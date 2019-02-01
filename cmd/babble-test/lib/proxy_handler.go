package runtime

import (
	"fmt"

	"github.com/mosaicnetworks/babble/src/crypto"
	"github.com/mosaicnetworks/babble/src/hashgraph"
	"github.com/mosaicnetworks/babble/src/proxy"
)

var tx string

type Handler struct {
	stateHash []byte
	com       chan []byte
}

// Called when a new block is comming
// You must provide a method to compute the stateHash incrementaly with incoming blocks
func (h *Handler) CommitHandler(block hashgraph.Block) (proxy.CommitResponse, error) {
	hash := h.stateHash

	for _, tx := range block.Transactions() {
		hash = crypto.SimpleHashFromTwoHashes(hash, crypto.SHA256(tx))

		fmt.Println(string(tx))
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

func NewHandler() *Handler {
	handler := &Handler{
		stateHash: []byte{},
		com:       make(chan []byte),
	}

	handler.Listen()

	return handler
}

func (h *Handler) Listen() {
	// for msg := range h.com {
	// 	// h.SubmitTx()
	// }

}
