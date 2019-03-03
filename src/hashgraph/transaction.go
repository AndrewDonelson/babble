package hashgraph

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"log"
	"strings"
	"github.com/AndrewDonelson/babble/src/wallets"
)

// Transaction ...
type Transaction struct {
	ID        []byte `json: "id"`
	Protocol  uint   `json:"protocol"`
	Payload   []byte `json:"payload"`
	Fee       uint64 `json: "fee"`
	Signature []byte `json: "signature"`
	PubKey    []byte `json: "pubkey"`
}

// Serialize returns a serialized Transaction
func (tx Transaction) Serialize() []byte {
	var encoded bytes.Buffer

	enc := gob.NewEncoder(&encoded)
	err := enc.Encode(tx)
	if err != nil {
		log.Panic(err)
	}

	return encoded.Bytes()
}

// Hash returns the hash of the Transaction
func (tx *Transaction) Hash() []byte {
	var hash [32]byte

	txCopy := *tx
	txCopy.ID = []byte{}

	hash = sha256.Sum256(txCopy.Serialize())

	return hash[:]
}

// UsesKey checks whether the address initiated the transaction
func (tx Transaction) UsesKey(pubKeyHash []byte) bool {
	lockingHash := HashPubKey(in.PubKey)

	return bytes.Compare(lockingHash, pubKeyHash) == 0
}
// String returns a human-readable representation of a transaction
func (tx Transaction) String() string {
	var lines []string

	lines = append(lines, fmt.Sprintf("--- Transaction %x:", tx.ID))
	lines = append(lines, fmt.Sprintf("       Signature: %x", tx.Signature))
	lines = append(lines, fmt.Sprintf("       PubKey:    %x", tx.PubKey))
	lines = append(lines, fmt.Sprintf("       Protocol:  %s", tx.Protocol))
	lines = append(lines, fmt.Sprintf("       Payload:  %s", tx.Payload))
	lines = append(lines, fmt.Sprintf("       Fee:  %d", tx.Fee))

	return strings.Join(lines, "\n")
}
