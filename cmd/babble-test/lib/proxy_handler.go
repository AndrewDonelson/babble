package runtime

import (
	"github.com/mosaicnetworks/babble/src/crypto"
	"github.com/mosaicnetworks/babble/src/hashgraph"
	"github.com/mosaicnetworks/babble/src/proxy"
	"github.com/mosaicnetworks/babble/src/proxy/inmem"
	"github.com/sirupsen/logrus"
)

var tx string

type Handler struct {
	stateHash []byte
	out       chan []byte
}

func NewHandler(out chan []byte) *Handler {
	return &Handler{
		stateHash: []byte{},
		out:       out,
	}
}

// Called when a new block is comming
// You must provide a method to compute the stateHash incrementaly with incoming blocks
func (h *Handler) CommitHandler(block hashgraph.Block) (proxy.CommitResponse, error) {
	hash := h.stateHash

	for _, tx := range block.Transactions() {
		hash = crypto.SimpleHashFromTwoHashes(hash, crypto.SHA256(tx))

		h.out <- tx
	}

	h.stateHash = hash

	response := proxy.CommitResponse{
		StateHash: hash,
		// InternalTransactions: block.InternalTransactions(),
	}

	return response, nil
}

// Called when syncing with the network
func (h *Handler) SnapshotHandler(blockIndex int) (snapshot []byte, err error) {
	return []byte{}, nil
}

// Called when syncing with the network
func (h *Handler) RestoreHandler(snapshot []byte) (stateHash []byte, err error) {
	return []byte{}, nil
}

type InmemSocketProxy struct {
	*inmem.InmemProxy
	handler *Handler
	in      chan []byte
	out     chan []byte
}

//InmemSocketProxy instantiates an InemDummyClient
func NewInmemSocketProxy(logger *logrus.Logger) *InmemSocketProxy {
	// state := NewState(logger)

	in := make(chan []byte)
	out := make(chan []byte, 100)

	handler := NewHandler(out)

	proxy := inmem.NewInmemProxy(handler, logger)

	client := &InmemSocketProxy{
		InmemProxy: proxy,
		handler:    handler,
		in:         in,
		out:        out,
	}

	return client
}

//SubmitTx sends a transaction to the Babble node via the InmemProxy
func (c *InmemSocketProxy) SubmitTx(tx []byte) {
	c.InmemProxy.SubmitTx(tx)
}

//GetCommittedTransactions returns the state's list of transactions
func (c *InmemSocketProxy) GetCommittedTransactions() [][]byte {
	return [][]byte{}
}

func (c *InmemSocketProxy) GetOutChan() chan []byte {
	return c.out
}
