package node

import (
	"crypto/ecdsa"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/mosaicnetworks/babble/src/crypto"
	hg "github.com/mosaicnetworks/babble/src/hashgraph"
	"github.com/mosaicnetworks/babble/src/peers"
	"github.com/mosaicnetworks/babble/src/proxy"
	"github.com/sirupsen/logrus"
)

type Core struct {
	id     uint32
	key    *ecdsa.PrivateKey
	pubKey []byte
	hexID  string
	hg     *hg.Hashgraph

	peers        *peers.PeerSet //[PubKey] => id
	peerSelector PeerSelector
	selectorLock sync.Mutex

	//Hash and Index of this instance's head Event
	Head string
	Seq  int

	//AcceptedRound is the first Round to which this peer belongs. A node will
	//not create SelfEvents before reaching AcceptedRound.
	AcceptedRound int

	/*
		Events that are not tied to this node's Head. This is managed by
		the Sync method. If the gossip condition is false (there is nothing
		interesting to record), items are added to heads; if the gossip
		condition is true, items are removed from heads and used to record a new
		self-event. This functionality allows to not grow the hashgraph
		continuously when there is nothing to record.
	*/
	heads map[uint32]*hg.Event

	transactionPool     [][]byte
	selfBlockSignatures *hg.SigPool

	proxyCommitCallback proxy.CommitCallback

	logger *logrus.Entry
}

func NewCore(
	id uint32,
	key *ecdsa.PrivateKey,
	peers *peers.PeerSet,
	store hg.Store,
	proxyCommitCallback proxy.CommitCallback,
	logger *logrus.Logger) *Core {

	if logger == nil {
		logger = logrus.New()
		logger.Level = logrus.DebugLevel
	}
	logEntry := logger.WithField("id", id)

	peerSelector := NewRandomPeerSelector(peers, id)

	core := &Core{
		id:                  id,
		key:                 key,
		proxyCommitCallback: proxyCommitCallback,
		peers:               peers,
		peerSelector:        peerSelector,
		transactionPool:     [][]byte{},
		selfBlockSignatures: hg.NewSigPool(),
		heads:               make(map[uint32]*hg.Event),
		logger:              logEntry,
		Head:                "",
		Seq:                 -1,
		AcceptedRound:       -1,
	}

	core.hg = hg.NewHashgraph(store, core.Commit, logEntry)

	/*
		This will create roots and set PeerSet for round 0, which is not
		necessarily correct; what if this is a node that only joins the cluster
		on the go? Doesnt really matter because it's going to get Reset.
	*/
	core.hg.Init(peers)

	return core
}

func (c *Core) ID() uint32 {
	return c.id
}

func (c *Core) PubKey() []byte {
	if c.pubKey == nil {
		c.pubKey = crypto.FromECDSAPub(&c.key.PublicKey)
	}
	return c.pubKey
}

func (c *Core) HexID() string {
	if c.hexID == "" {
		pubKey := c.PubKey()
		c.hexID = fmt.Sprintf("0x%X", pubKey)
	}
	return c.hexID
}

func (c *Core) SetHeadAndSeq() error {
	var head string
	var seq int

	//Add self if not in Repertoire yet
	if _, ok := c.hg.Store.RepertoireByID()[c.ID()]; !ok {
		c.logger.Debug("Not in repertoire yet.")
		err := c.hg.Store.AddParticipant(peers.NewPeer(c.HexID(), ""))
		if err != nil {
			c.logger.WithError(err).Error("Error adding self to Store")
			return err
		}
	}

	last, isRoot, err := c.hg.Store.LastEventFrom(c.HexID())
	if err != nil {
		return err
	}

	if isRoot {
		root, err := c.hg.Store.GetRoot(c.HexID())
		if err != nil {
			return err
		}
		head = root.GetHead().Hash
		seq = root.GetHead().Index
	} else {
		lastEvent, err := c.GetEvent(last)
		if err != nil {
			return err
		}
		head = last
		seq = lastEvent.Index()
	}

	c.Head = head
	c.Seq = seq

	c.logger.WithFields(logrus.Fields{
		"core.Head": c.Head,
		"core.Seq":  c.Seq,
		"is_root":   isRoot,
	}).Debugf("SetHeadAndSeq")

	return nil
}

func (c *Core) Bootstrap() error {
	return c.hg.Bootstrap()
}

//++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++

func (c *Core) SignAndInsertSelfEvent(event *hg.Event) error {
	if c.hg.Store.LastRound() < c.AcceptedRound {
		c.logger.Debugf("Too early to gossip (%d / %d)", c.hg.Store.LastRound(), c.AcceptedRound)
		return nil
	}
	if err := event.Sign(c.key); err != nil {
		return err
	}
	return c.InsertEventAndRunConsensus(event, true)
}

func (c *Core) InsertEventAndRunConsensus(event *hg.Event, setWireInfo bool) error {
	if err := c.hg.InsertEventAndRunConsensus(event, setWireInfo); err != nil {
		return err
	}
	if event.Creator() == c.HexID() {
		c.Head = event.Hex()
		c.Seq = event.Index()
	}
	return nil
}

func (c *Core) KnownEvents() map[uint32]int {
	return c.hg.Store.KnownEvents()
}

//++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++

func (c *Core) Commit(block *hg.Block) error {
	//Commit the Block to the App
	commitResponse, err := c.proxyCommitCallback(*block)

	c.logger.WithFields(logrus.Fields{
		"block":      block.Index(),
		"state_hash": fmt.Sprintf("%X", commitResponse.StateHash),
		"err":        err,
	}).Debug("CommitBlock Response")

	//Handle the response to set Block StateHash and process accepted
	//InternalTransactions which might update the PeerSet.
	if err == nil {
		block.Body.StateHash = commitResponse.StateHash

		sig, err := c.SignBlock(block)
		if err != nil {
			return err
		}

		err = c.hg.SetAnchorBlock(block)
		if err != nil {
			return err
		}

		c.selfBlockSignatures.Add(sig)
	}

	return err
}

func (c *Core) SignBlock(block *hg.Block) (hg.BlockSignature, error) {
	sig, err := block.Sign(c.key)
	if err != nil {
		return hg.BlockSignature{}, err
	}

	err = block.SetSignature(sig)
	if err != nil {
		return hg.BlockSignature{}, err
	}

	err = c.hg.Store.SetBlock(block)
	if err != nil {
		return sig, err
	}

	return sig, nil
}

//++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++

func (c *Core) OverSyncLimit(knownEvents map[uint32]int, syncLimit int) bool {
	totUnknown := 0
	myKnownEvents := c.KnownEvents()
	for i, li := range myKnownEvents {
		if li > knownEvents[i] {
			totUnknown += li - knownEvents[i]
		}
	}
	if totUnknown > syncLimit {
		return true
	}
	return false
}

func (c *Core) GetAnchorBlockWithFrame() (*hg.Block, *hg.Frame, error) {
	return c.hg.GetAnchorBlockWithFrame()
}

//returns events that c knowns about and are not in 'known'
func (c *Core) EventDiff(known map[uint32]int) (events []*hg.Event, err error) {
	unknown := []*hg.Event{}
	//known represents the index of the last event known for every participant
	//compare this to our view of events and fill unknown with events that we know of
	// and the other doesnt
	for id, ct := range known {
		peer, ok := c.peers.ByID[id]

		if !ok {
			continue
		}

		//get participant Events with index > ct
		participantEvents, err := c.hg.Store.ParticipantEvents(peer.PubKeyHex, ct)
		if err != nil {
			return []*hg.Event{}, err
		}
		for _, e := range participantEvents {
			ev, err := c.hg.Store.GetEvent(e)
			if err != nil {
				return []*hg.Event{}, err
			}
			unknown = append(unknown, ev)
		}
	}
	sort.Sort(hg.ByTopologicalOrder(unknown))

	return unknown, nil
}

//Sync decodes and inserts new Events into the Hashgraph. UnknownEvents are
//expected to be in topoligical order.
func (c *Core) Sync(fromID uint32, unknownEvents []hg.WireEvent) error {
	c.logger.WithFields(logrus.Fields{
		"unknown_events":      len(unknownEvents),
		"transaction_pool":    len(c.transactionPool),
		"self_signature_pool": c.selfBlockSignatures.Len(),
	}).Debug("Sync")

	var otherHead *hg.Event
	for _, we := range unknownEvents {
		ev, err := c.hg.ReadWireInfo(we)
		if err != nil {
			c.logger.WithFields(logrus.Fields{
				"wire_event": we,
				"error":      err,
			}).Error("Reading WireEvent")
			return err
		}

		if err := c.InsertEventAndRunConsensus(ev, false); err != nil {
			c.logger.WithError(err).Errorf("Inserting Event")
			return err
		}

		if we.Body.CreatorID == fromID {
			otherHead = ev
		}

		if h, ok := c.heads[we.Body.CreatorID]; ok &&
			h != nil &&
			we.Body.Index > h.Index() {

			delete(c.heads, we.Body.CreatorID)
		}
	}

	//Do not overwrite a non-empty head with an empty head
	if h, ok := c.heads[fromID]; !ok ||
		h == nil ||
		(otherHead != nil && otherHead.Index() > h.Index()) {

		c.heads[fromID] = otherHead
	}

	//Create new event with self head and other head only if there are pending
	//loaded events or the pools are not empty
	if c.hg.PendingLoadedEvents > 0 ||
		len(c.transactionPool) > 0 ||
		c.selfBlockSignatures.Len() > 0 {

		return c.RecordHeads()
	}

	return nil
}

func (c *Core) RecordHeads() error {
	c.logger.WithField("heads", len(c.heads)).Debug("RecordHeads()")

	for id, ev := range c.heads {
		op := ""
		if ev != nil {
			op = ev.Hex()
		}
		if err := c.AddSelfEvent(op); err != nil {
			return err
		}
		delete(c.heads, id)
	}

	return nil
}

func (c *Core) AddSelfEvent(otherHead string) error {
	//Add own block signatures to next Event
	sigs := c.selfBlockSignatures.Slice()

	//create new event with self head and otherHead
	//empty pools in its payload
	newHead := hg.NewEvent(c.transactionPool,
		sigs,
		[]string{c.Head, otherHead},
		c.PubKey(), c.Seq+1)

	if err := c.SignAndInsertSelfEvent(newHead); err != nil {
		c.logger.WithError(err).Errorf("Error inserting new head")
		return err
	}

	c.logger.WithFields(logrus.Fields{
		"loaded_events":         c.hg.PendingLoadedEvents,
		"transactions":          len(c.transactionPool),
		"self_block_signatures": len(sigs),
	}).Debug("Created Self-Event")

	c.transactionPool = [][]byte{}
	c.selfBlockSignatures.RemoveSlice(sigs)

	return nil
}

func (c *Core) FastForward(peer string, block *hg.Block, frame *hg.Frame) error {
	peerSet := peers.NewPeerSet(frame.Peers)

	//Check Block Signatures
	err := c.hg.CheckBlock(block, peerSet)
	if err != nil {
		return err
	}

	//Check Frame Hash
	frameHash, err := frame.Hash()
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(block.FrameHash(), frameHash) {
		return fmt.Errorf("Invalid Frame Hash")
	}

	err = c.hg.Reset(block, frame)
	if err != nil {
		return err
	}

	err = c.SetHeadAndSeq()
	if err != nil {
		return err
	}

	return nil
}

func (c *Core) FromWire(wireEvents []hg.WireEvent) ([]hg.Event, error) {
	events := make([]hg.Event, len(wireEvents), len(wireEvents))

	for i, w := range wireEvents {
		ev, err := c.hg.ReadWireInfo(w)
		if err != nil {
			return nil, err
		}

		events[i] = *ev
	}

	return events, nil
}

func (c *Core) ToWire(events []*hg.Event) ([]hg.WireEvent, error) {
	wireEvents := make([]hg.WireEvent, len(events), len(events))

	for i, e := range events {
		wireEvents[i] = e.ToWire()
	}

	return wireEvents, nil
}

func (c *Core) ProcessSigPool() error {
	return c.hg.ProcessSigPool()
}

func (c *Core) AddTransactions(txs [][]byte) {
	c.transactionPool = append(c.transactionPool, txs...)
}

func (c *Core) GetHead() (*hg.Event, error) {
	return c.hg.Store.GetEvent(c.Head)
}

func (c *Core) GetEvent(hash string) (*hg.Event, error) {
	return c.hg.Store.GetEvent(hash)
}

func (c *Core) GetEventTransactions(hash string) ([][]byte, error) {
	var txs [][]byte
	ex, err := c.GetEvent(hash)
	if err != nil {
		return txs, err
	}
	txs = ex.Transactions()
	return txs, nil
}

func (c *Core) GetConsensusEvents() []string {
	return c.hg.Store.ConsensusEvents()
}

func (c *Core) GetConsensusEventsCount() int {
	return c.hg.Store.ConsensusEventsCount()
}

func (c *Core) GetUndeterminedEvents() []string {
	return c.hg.UndeterminedEvents
}

func (c *Core) GetPendingLoadedEvents() int {
	return c.hg.PendingLoadedEvents
}

func (c *Core) GetConsensusTransactions() ([][]byte, error) {
	txs := [][]byte{}
	for _, e := range c.GetConsensusEvents() {
		eTxs, err := c.GetEventTransactions(e)
		if err != nil {
			return txs, fmt.Errorf("Consensus event not found: %s", e)
		}
		txs = append(txs, eTxs...)
	}
	return txs, nil
}

func (c *Core) GetLastConsensusRoundIndex() *int {
	return c.hg.LastConsensusRound
}

func (c *Core) GetConsensusTransactionsCount() int {
	return c.hg.ConsensusTransactions
}

func (c *Core) GetLastCommitedRoundEventsCount() int {
	return c.hg.LastCommitedRoundEvents
}

func (c *Core) GetLastBlockIndex() int {
	return c.hg.Store.LastBlockIndex()
}
