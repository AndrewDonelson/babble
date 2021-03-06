package node

import (
	"crypto/ecdsa"
	"fmt"
	"strconv"
	"sync"
	"time"

	hg "github.com/mosaicnetworks/babble/src/hashgraph"
	"github.com/mosaicnetworks/babble/src/net"
	"github.com/mosaicnetworks/babble/src/peers"
	"github.com/mosaicnetworks/babble/src/proxy"
	"github.com/sirupsen/logrus"
)

type Node struct {
	nodeState

	conf   *Config
	logger *logrus.Entry

	id       uint32
	core     *Core
	coreLock sync.Mutex

	trans net.Transport
	netCh <-chan net.RPC

	proxy    proxy.AppProxy
	submitCh chan []byte

	shutdownCh chan struct{}

	controlTimer *ControlTimer

	start        time.Time
	syncRequests int
	syncErrors   int

	needBoostrap bool
}

func NewNode(conf *Config,
	id uint32,
	key *ecdsa.PrivateKey,
	peers *peers.PeerSet,
	store hg.Store,
	trans net.Transport,
	proxy proxy.AppProxy,
) *Node {

	node := Node{
		id:           id,
		conf:         conf,
		logger:       conf.Logger.WithField("this_id", id),
		core:         NewCore(id, key, peers, store, proxy.CommitBlock, conf.Logger),
		trans:        trans,
		netCh:        trans.Consumer(),
		proxy:        proxy,
		submitCh:     proxy.SubmitCh(),
		shutdownCh:   make(chan struct{}),
		controlTimer: NewRandomControlTimer(),
	}

	node.needBoostrap = store.NeedBoostrap()

	return &node
}

func (n *Node) Init() error {
	if n.needBoostrap {
		n.logger.Debug("Bootstrap")

		if err := n.core.Bootstrap(); err != nil {
			return err
		}
	}

	if _, ok := n.core.peers.ByID[n.id]; !ok {
		n.logger.Debug("Node does not belong to PeerSet => Shutdown")
	}

	if err := n.core.SetHeadAndSeq(); err != nil {
		n.core.SetHeadAndSeq()
	}

	n.setState(Babbling)

	return nil
}

func (n *Node) RunAsync(gossip bool) {
	n.logger.WithField("gossip", gossip).Debug("runasync")

	go n.Run(gossip)
}

func (n *Node) Run(gossip bool) {
	//The ControlTimer allows the background routines to control the
	//heartbeat timer when the node is in the Babbling state. The timer should
	//only be running when there are uncommitted transactions in the system.
	go n.controlTimer.Run(n.conf.HeartbeatTimeout)

	//Execute some background work regardless of the state of the node.
	go n.doBackgroundWork()

	//Execute Node State Machine
	for {
		//Run different routines depending on node state
		state := n.getState()

		n.logger.WithField("state", state.String()).Debug("Run loop")

		switch state {
		case Babbling:
			n.babble(gossip)
		case CatchingUp:
			n.fastForward()
		case Shutdown:
			return
		}
	}
}

func (n *Node) resetTimer() {
	n.coreLock.Lock()
	defer n.coreLock.Unlock()

	if !n.controlTimer.set {
		ts := n.conf.HeartbeatTimeout

		//Slow gossip if nothing interesting to say
		if n.core.hg.PendingLoadedEvents == 0 &&
			len(n.core.transactionPool) == 0 &&
			n.core.selfBlockSignatures.Len() == 0 {
			ts = time.Duration(time.Second)
		}

		n.controlTimer.resetCh <- ts
	}
}

func (n *Node) doBackgroundWork() {
	for {
		select {
		case t := <-n.submitCh:
			n.logger.Debug("Adding Transaction")
			n.addTransaction(t)
			n.resetTimer()
		case <-n.shutdownCh:
			return
		}
	}
}

//babble is interrupted when a gossip function, launched asychronously, changes
//the state from Babbling to CatchingUp, or when the node is shutdown.
//Otherwise, it processes RPC requests, periodicaly initiates gossip while there
//is something to gossip about, or waits.
func (n *Node) babble(gossip bool) {
	n.logger.Debug("BABBLING")

	returnCh := make(chan struct{}, 100)
	for {
		select {
		case rpc := <-n.netCh:
			n.goFunc(func() {
				n.logger.Debug("Processing RPC")
				n.processRPC(rpc)
				n.resetTimer()
			})
		case <-n.controlTimer.tickCh:
			if gossip {
				n.logger.Debug("Time to gossip!")
				peer := n.core.peerSelector.Next()
				if peer != nil {
					n.goFunc(func() { n.gossip(peer, returnCh) })
				} else {
					n.monologue()
				}
			}
			n.resetTimer()
		case <-returnCh:
			return
		case <-n.shutdownCh:
			return
		}
	}
}

func (n *Node) fastForward() error {
	n.logger.Debug("CATCHING-UP")

	//wait until sync routines finish
	n.waitRoutines()

	//fastForwardRequest
	peer := n.core.peerSelector.Next()

	start := time.Now()
	resp, err := n.requestFastForward(peer.NetAddr)
	elapsed := time.Since(start)
	n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("requestFastForward()")
	if err != nil {
		n.logger.WithField("error", err).Error("requestFastForward()")
		return err
	}

	n.logger.WithFields(logrus.Fields{
		"from_id":              resp.FromID,
		"block_index":          resp.Block.Index(),
		"block_round_received": resp.Block.RoundReceived(),
		"frame_events":         len(resp.Frame.Events),
		"frame_roots":          resp.Frame.Roots,
		"frame_peers":          len(resp.Frame.Peers),
		"snapshot":             resp.Snapshot,
	}).Debug("FastForwardResponse")

	//prepare core. ie: fresh hashgraph
	n.coreLock.Lock()
	err = n.core.FastForward(peer.PubKeyHex, &resp.Block, &resp.Frame)
	n.coreLock.Unlock()
	if err != nil {
		n.logger.WithError(err).Error("Fast Forwarding Hashgraph")
		return err
	}

	//update app from snapshot
	err = n.proxy.Restore(resp.Snapshot)
	if err != nil {
		n.logger.WithError(err).Error("Restoring App from Snapshot")
		return err
	}

	n.logger.Debug("Fast-Forward OK")

	n.setState(Babbling)

	return nil
}

//This function is usually called in a go-routine and needs to inform the
//calling routine (usually the babble routine) when it is time to exit the
//Babbling state and return.
func (n *Node) gossip(peer *peers.Peer, parentReturnCh chan struct{}) error {
	//pull
	syncLimit, otherKnownEvents, err := n.pull(peer)
	if err != nil {
		n.logger.WithError(err).Error("gossip pull")
		return err
	}

	//check and handle syncLimit
	if syncLimit {
		n.logger.WithField("from", peer.ID()).Debug("SyncLimit")
		n.setState(CatchingUp) //
		parentReturnCh <- struct{}{}

		return nil
	}

	//push
	err = n.push(peer, otherKnownEvents)
	if err != nil {
		n.logger.WithError(err).Error("gossip push")
		return err
	}

	//update peer selector
	n.core.selectorLock.Lock()
	n.core.peerSelector.UpdateLast(peer.ID())
	n.core.selectorLock.Unlock()

	n.logStats()

	return nil
}

func (n *Node) monologue() error {
	n.coreLock.Lock()
	defer n.coreLock.Unlock()

	err := n.core.AddSelfEvent("")
	if err != nil {
		n.logger.WithError(err).Error("monologue, AddSelfEvent()")
		return err
	}

	err = n.core.ProcessSigPool()
	if err != nil {
		n.logger.WithError(err).Error("monologue, ProcessSigPool()")
		return err
	}

	return nil
}

func (n *Node) pull(peer *peers.Peer) (syncLimit bool, otherKnownEvents map[uint32]int, err error) {
	//Compute Known
	n.coreLock.Lock()
	knownEvents := n.core.KnownEvents()
	n.coreLock.Unlock()

	//Send SyncRequest
	start := time.Now()
	resp, err := n.requestSync(peer.NetAddr, knownEvents)
	elapsed := time.Since(start)
	n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("requestSync()")

	if err != nil {
		n.logger.WithField("error", err).Error("requestSync()")
		return false, nil, err
	}

	n.logger.WithFields(logrus.Fields{
		"from_id":    resp.FromID,
		"sync_limit": resp.SyncLimit,
		"events":     len(resp.Events),
		"known":      resp.Known,
	}).Debug("SyncResponse")

	if resp.SyncLimit {
		return true, nil, nil
	}

	//Add Events to Hashgraph and create new Head if necessary
	n.coreLock.Lock()
	err = n.sync(peer.ID(), resp.Events)
	n.coreLock.Unlock()

	if err != nil {
		n.logger.WithField("error", err).Error("sync()")
		return false, nil, err
	}

	return false, resp.Known, nil
}

func (n *Node) push(peer *peers.Peer, knownEvents map[uint32]int) error {
	//Check SyncLimit
	n.coreLock.Lock()
	overSyncLimit := n.core.OverSyncLimit(knownEvents, n.conf.SyncLimit)
	n.coreLock.Unlock()

	if overSyncLimit {
		n.logger.Debug("SyncLimit")
		return nil
	}

	//Compute Diff
	start := time.Now()
	n.coreLock.Lock()
	eventDiff, err := n.core.EventDiff(knownEvents)
	n.coreLock.Unlock()
	elapsed := time.Since(start)
	n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("Diff()")
	if err != nil {
		n.logger.WithField("error", err).Error("Calculating Diff")
		return err
	}

	if len(eventDiff) > 0 {
		//Convert to WireEvents
		wireEvents, err := n.core.ToWire(eventDiff)
		if err != nil {
			n.logger.WithField("error", err).Debug("Converting to WireEvent")
			return err
		}

		//Create and Send EagerSyncRequest
		start = time.Now()
		resp2, err := n.requestEagerSync(peer.NetAddr, wireEvents)
		elapsed = time.Since(start)
		n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("requestEagerSync()")
		if err != nil {
			n.logger.WithField("error", err).Error("requestEagerSync()")
			return err
		}
		n.logger.WithFields(logrus.Fields{
			"from_id": resp2.FromID,
			"success": resp2.Success,
		}).Debug("EagerSyncResponse")
	}

	return nil
}

func (n *Node) sync(fromID uint32, events []hg.WireEvent) error {
	//Insert Events in Hashgraph and create new Head if necessary
	start := time.Now()
	err := n.core.Sync(fromID, events)
	elapsed := time.Since(start)
	n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("Sync()")
	if err != nil {
		n.logger.WithError(err).Error()
		return err
	}

	//Process SignaturePool
	start = time.Now()
	err = n.core.ProcessSigPool()
	elapsed = time.Since(start)
	n.logger.WithField("duration", elapsed.Nanoseconds()).Debug("ProcessSigPool()")
	if err != nil {
		n.logger.WithError(err).Error()
		return err
	}

	return nil
}

func (n *Node) addTransaction(tx []byte) {
	n.coreLock.Lock()
	defer n.coreLock.Unlock()

	n.core.AddTransactions([][]byte{tx})
}

func (n *Node) Shutdown() {
	if n.getState() != Shutdown {
		n.logger.Debug("Shutdown")

		//Exit any non-shutdown state immediately
		n.setState(Shutdown)

		//Stop and wait for concurrent operations
		close(n.shutdownCh)

		n.waitRoutines()

		//For some reason this needs to be called after closing the shutdownCh
		//Not entirely sure why...
		n.controlTimer.Shutdown()

		//transport and store should only be closed once all concurrent operations
		//are finished otherwise they will panic trying to use close objects
		n.trans.Close()

		n.core.hg.Store.Close()
	}
}

func (n *Node) GetStats() map[string]string {
	toString := func(i *int) string {
		if i == nil {
			return "nil"
		}

		return strconv.Itoa(*i)
	}

	timeElapsed := time.Since(n.start)

	consensusEvents := n.core.GetConsensusEventsCount()

	consensusEventsPerSecond := float64(consensusEvents) / timeElapsed.Seconds()

	lastConsensusRound := n.core.GetLastConsensusRoundIndex()

	var consensusRoundsPerSecond float64

	if lastConsensusRound != nil {
		consensusRoundsPerSecond = float64(*lastConsensusRound) / timeElapsed.Seconds()
	}

	s := map[string]string{
		"last_consensus_round":   toString(lastConsensusRound),
		"last_block_index":       strconv.Itoa(n.core.GetLastBlockIndex()),
		"consensus_events":       strconv.Itoa(consensusEvents),
		"consensus_transactions": strconv.Itoa(n.core.GetConsensusTransactionsCount()),
		"undetermined_events":    strconv.Itoa(len(n.core.GetUndeterminedEvents())),
		"transaction_pool":       strconv.Itoa(len(n.core.transactionPool)),
		"num_peers":              strconv.Itoa(n.core.peerSelector.Peers().Len()),
		"sync_rate":              strconv.FormatFloat(n.SyncRate(), 'f', 2, 64),
		"events_per_second":      strconv.FormatFloat(consensusEventsPerSecond, 'f', 2, 64),
		"rounds_per_second":      strconv.FormatFloat(consensusRoundsPerSecond, 'f', 2, 64),
		"round_events":           strconv.Itoa(n.core.GetLastCommitedRoundEventsCount()),
		"id":                     fmt.Sprint(n.id),
		"state":                  n.getState().String(),
	}
	return s
}

func (n *Node) logStats() {
	stats := n.GetStats()

	n.logger.WithFields(logrus.Fields{
		"last_consensus_round":   stats["last_consensus_round"],
		"last_block_index":       stats["last_block_index"],
		"consensus_events":       stats["consensus_events"],
		"consensus_transactions": stats["consensus_transactions"],
		"undetermined_events":    stats["undetermined_events"],
		"transaction_pool":       stats["transaction_pool"],
		"num_peers":              stats["num_peers"],
		"sync_rate":              stats["sync_rate"],
		"events/s":               stats["events_per_second"],
		"rounds/s":               stats["rounds_per_second"],
		"round_events":           stats["round_events"],
		"id":                     stats["id"],
		"state":                  stats["state"],
	}).Debug("Stats")
}

func (n *Node) SyncRate() float64 {
	var syncErrorRate float64

	if n.syncRequests != 0 {
		syncErrorRate = float64(n.syncErrors) / float64(n.syncRequests)
	}

	return 1 - syncErrorRate
}

func (n *Node) GetBlock(blockIndex int) (*hg.Block, error) {
	return n.core.hg.Store.GetBlock(blockIndex)
}

func (n *Node) GetEvents() (map[uint32]int, error) {
	res := n.core.KnownEvents()

	return res, nil
}

func (n *Node) ID() uint32 {
	return n.id
}

func (n *Node) GetPeers() []*peers.Peer {
	return n.core.peers.Peers
}
