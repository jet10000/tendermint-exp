package ticketstore

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/cbergoon/merkletree"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	sha3 "github.com/miguelmota/go-solidity-sha3"
	"github.com/tendermint/tendermint/abci/types"
	"log"
	"strconv"
	"strings"
)

const (
	codeTypeOK            	uint32 = 0
	codeTypeEncodingError	uint32 = 1
	codeTypeTicketError  	uint32 = 2
)

var (
	ErrBadAddress = &ticketError{"Ticket must have an address"}
	ErrBadNonce   = &ticketError{"Ticket nonce must increase on resale"}
	ErrBadSignature = &ticketError{"Resale must be signed by the previous owner"}
)

type ticketError struct{ msg string }

func (err ticketError) Error() string { return err.msg }

type state struct {
	size         int64
	height       int64
	rootHash     []byte
	tickets      map[uint64]Ticket
	tempTreeContent  []merkletree.Content
}

type Ticket struct {
	Id            	uint64  `json:"id"`
	Nonce           uint64  `json:"nonce"`
	Details       	string  `json:"details"`
	OwnerAddr     	string  `json:"ownerAddr"`
	PrevOwnerProof  string  `json:"prevOwnerProof"`
}

type TicketStoreApplication struct {
	types.BaseApplication
	state state
}

func NewTicketStoreApplication() *TicketStoreApplication {
	return &TicketStoreApplication{state: state{tickets: make(map[uint64]Ticket)}}
}

func (app *TicketStoreApplication) Info(req types.RequestInfo) types.ResponseInfo {
	return types.ResponseInfo{
		Data: fmt.Sprintf("{\"hashes\":%v,\"tickets\":%v}", app.state.height, app.state.size),
		LastBlockHeight: app.state.height,
		LastBlockAppHash: app.state.rootHash}
}

func (app *TicketStoreApplication) DeliverTx(tx []byte) types.ResponseDeliverTx {
	var ticket Ticket
	err := json.Unmarshal(tx, &ticket)

	if err != nil {
		return types.ResponseDeliverTx{
			Code: codeTypeEncodingError,
			Log: fmt.Sprint(err) }
	}

	previousTicket := app.state.tickets[ticket.Id]
	err = ticket.validate(previousTicket)
	if err != nil {
		return types.ResponseDeliverTx{
			Code: codeTypeTicketError,
			Log: fmt.Sprint(err) }
	}

	app.state.size++
	app.state.tickets[ticket.Id] = ticket
	app.state.tempTreeContent = append(app.state.tempTreeContent, ticket)
	return types.ResponseDeliverTx{Code: codeTypeOK}
}

func (app *TicketStoreApplication) CheckTx(tx []byte) types.ResponseCheckTx {
	var ticket Ticket
	err := json.Unmarshal(tx, &ticket)

	if err != nil {
		return types.ResponseCheckTx{
			Code: codeTypeEncodingError,
			Log: fmt.Sprint(err) }
	}

	previousTicket := app.state.tickets[ticket.Id]
	err = ticket.validate(previousTicket)
	if err != nil {
		return types.ResponseCheckTx{
			Code: codeTypeTicketError,
			Log: fmt.Sprint(err) }
	}

	return types.ResponseCheckTx{Code: codeTypeOK}
}

func (app *TicketStoreApplication) Commit() (resp types.ResponseCommit) {
	app.state.height++

	if len(app.state.tempTreeContent) > 0 {
		tree, _ := merkletree.NewTree(app.state.tempTreeContent)
		app.state.rootHash = tree.Root.Hash
		app.state.tempTreeContent = app.state.tempTreeContent[:0]
	}

	return types.ResponseCommit{Data: app.state.rootHash}
}

func (app *TicketStoreApplication) Query(reqQuery types.RequestQuery) types.ResponseQuery {
	switch reqQuery.Path {
	case "hash":
		return types.ResponseQuery{Value: []byte(fmt.Sprintf("%v", app.state.height))}
	case "tx":
		return types.ResponseQuery{Value: []byte(fmt.Sprintf("%v", app.state.size))}
	case "ticket":
		id, err := strconv.ParseUint(string(reqQuery.Data), 10, 64)
		if err != nil {
			return types.ResponseQuery{Log: fmt.Sprintf("%v is not a valid ticket id", reqQuery.Data)}
		}
		return types.ResponseQuery{Value: []byte(fmt.Sprintf("%v", app.state.tickets[id]))}
	default:
		return types.ResponseQuery{Log: fmt.Sprintf("Invalid query path. Expected hash or tx, got %v", reqQuery.Path)}
	}
}

func (ticket Ticket) CalculateHash() ([]byte, error) {
	idBytes := make([]byte,8)
	binary.LittleEndian.PutUint64(idBytes, ticket.Id)
	hash := sha3.SoliditySHA3(
		[]string{"uint256", "uint256", "string", "address", "bytes"},
		[]interface{}{fmt.Sprint(ticket.Id), fmt.Sprint(ticket.Nonce), ticket.Details, ticket.OwnerAddr, ticket.PrevOwnerProof})
	return hash, nil
}

func (ticket Ticket) Equals(other merkletree.Content) (bool, error) {
	otherTicket, isTicket := other.(Ticket)
	if isTicket {
		return ticket == otherTicket, nil
	}

	return false, fmt.Errorf("%v is not a ticket", other)
}


func (ticket Ticket) validate(prevTicket Ticket) error {
	if ticket.OwnerAddr == "" {
		return ErrBadAddress
	}

	if ticket.Nonce <= prevTicket.Nonce {
		return ErrBadNonce
	}

	if prevTicket.OwnerAddr != "" {
		prevTicketHash, err := prevTicket.CalculateHash()
		if err != nil {
			return err
		}

		signer, err := ticket.getOwnerProofSigner(prevTicketHash)
		if err != nil {
			return err
		}
		log.Print(signer, prevTicket.OwnerAddr)
		if signer != strings.ToLower(prevTicket.OwnerAddr) {
			return ErrBadSignature
		}
	}

	return nil
}

func (ticket Ticket) getOwnerProofSigner(prevTicketHash []byte) (string, error) {
	bytesProof, err := hexutil.Decode(ticket.PrevOwnerProof)
	if err != nil {
		return "", err
	}

	bytesProof[64] -= 27
	signerPkey, err := crypto.SigToPub(prevTicketHash, bytesProof)
	if err != nil {
		return "", err
	}

	return strings.ToLower(crypto.PubkeyToAddress(*signerPkey).Hex()), nil
}