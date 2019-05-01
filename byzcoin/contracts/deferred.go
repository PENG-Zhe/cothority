package contracts

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"go.dedis.ch/cothority/v3/byzcoin"
	"go.dedis.ch/cothority/v3/darc"
	"go.dedis.ch/protobuf"
)

// The deferred contract allows a group of signers to agree on and sign a
// proposed transaction, the "proposed transaction".

// ContractDeferredID denotes a contract that can aggregate signatures for a
// "proposed" transaction
var ContractDeferredID = "deferred"

const defaultNumExecution uint64 = 1

// deferredData contains the specific data of a deferred contract
type deferredData struct {
	// The transaction that signers must sign and can be executed with an
	// "executeProposedTx".
	ProposedTransaction byzcoin.ClientTransaction
	// If the current block index is greater than this value, any Invoke on the
	// deferred contract is rejected. This provides an expiration mechanism.
	ExpireBlockIndex uint64
	// Hashes of each instruction of the proposed transaction. Those hashes are
	// computed using the special "hashDeferred" method.
	InstructionHashes [][]byte
	// The number of time the proposed transaction can be executed. This number
	// decreases for each successful invocation of "executeProposedTx" and its
	// default value is set to 1.
	NumExecution uint64
	// This array is filled with the instruction IDs of each executed
	// instruction when a successful "executeProposedTx" happens.
	ExecResult [][]byte
}

type contractDeferred struct {
	byzcoin.BasicContract
	deferredData
	s *byzcoin.Service
}

func (s *Service) contractDeferredFromBytes(in []byte) (byzcoin.Contract, error) {
	c := &contractDeferred{s: s.byzService()}

	err := protobuf.Decode(in, &c.deferredData)
	if err != nil {
		return nil, errors.New("couldn't unmarshal instance data: " + err.Error())
	}
	return c, nil
}

func (c *contractDeferred) Spawn(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, coins []byzcoin.Coin) (sc []byzcoin.StateChange, cout []byzcoin.Coin, err error) {
	// This method should do the following:
	//   1. Parse the input buffer
	//   2. Compute and store the instruction hashes
	//   3. Save the data
	//
	// Spawn should have those input arguments:
	//   - proposedTransaction ClientTransaction
	//   - expireBlockIndex uint64
	//   - numExecution uint64 (default: 1)
	cout = coins

	// Find the darcID for this instance.
	var darcID darc.ID
	_, _, _, darcID, err = rst.GetValues(inst.InstanceID.Slice())
	if err != nil {
		return
	}

	// 1. Reads and parses the input
	proposedTransaction := byzcoin.ClientTransaction{}
	err = protobuf.Decode(inst.Spawn.Args.Search("proposedTransaction"), &proposedTransaction)
	if err != nil {
		return nil, nil, errors.New("couldn't decode proposedTransaction: " + err.Error())
	}
	expireBlockIndex, err := strconv.ParseUint(string(inst.Spawn.Args.Search("expireBlockIndex")), 10, 64)
	if err != nil {
		return nil, nil, errors.New("couldn't convert expireBlockIndex: " + err.Error())
	}
	numExecutionBuff := inst.Spawn.Args.Search("NumExecution")
	NumExecution := defaultNumExecution
	if len(numExecutionBuff) > 0 {
		NumExecution, err = strconv.ParseUint(string(numExecutionBuff), 10, 64)
		if err != nil {
			return nil, nil, errors.New("couldn't parse NumExecution: " + err.Error())
		}
	}

	// 2. Computes the hashes of each instruction and store it
	hash := make([][]byte, len(proposedTransaction.Instructions))
	for i, proposedInstruction := range proposedTransaction.Instructions {
		hash[i] = hashDeferred(proposedInstruction, inst.InstanceID.Slice())
	}

	// 3. Saves the data
	data := deferredData{
		ProposedTransaction: proposedTransaction,
		ExpireBlockIndex:    expireBlockIndex,
		InstructionHashes:   hash,
		NumExecution:        NumExecution,
	}
	var dataBuf []byte
	dataBuf, err = protobuf.Encode(&data)
	if err != nil {
		return nil, nil, errors.New("couldn't encode deferredData: " + err.Error())
	}

	sc = append(sc, byzcoin.NewStateChange(byzcoin.Create, inst.DeriveID(""),
		ContractDeferredID, dataBuf, darcID))
	return
}

func (c *contractDeferred) Invoke(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, coins []byzcoin.Coin) (sc []byzcoin.StateChange, cout []byzcoin.Coin, err error) {
	// This method should do the following:
	//   - Handle the "addProof" invocation
	//   - Handle the "execProposedTx" invocation
	//
	// Invoke:addProof should have the following input argument:
	//   - identity darc.Identity
	//   - signature []byte
	//	 - index uint32 (index of the instruction wrt the transaction)
	cout = coins

	// Find the darcID for this instance.
	var darcID darc.ID

	_, _, _, darcID, err = rst.GetValues(inst.InstanceID.Slice())
	if err != nil {
		return
	}

	switch inst.Invoke.Command {
	case "addProof":
		// This invocation appends the identity and the corresponding signature,
		// which is based on the stored instruction hash (in instructionHashes)

		// Get the given index
		indexBuf := inst.Invoke.Args.Search("index")
		if indexBuf == nil {
			return nil, nil, errors.New("Index args is nil")
		}
		index := binary.LittleEndian.Uint32(indexBuf)

		// Check if the index is in range
		numInstruction := len(c.deferredData.ProposedTransaction.Instructions)
		if index >= uint32(numInstruction) {
			return nil, nil, fmt.Errorf("Index is out of range (%d >= %d)", index, numInstruction)
		}

		// Get the given Identity
		identityBuf := inst.Invoke.Args.Search("identity")
		if identityBuf == nil {
			return nil, nil, errors.New("Identity args is nil")
		}
		identity := darc.Identity{}
		err = protobuf.Decode(identityBuf, &identity)
		if err != nil {
			return nil, nil, errors.New("Couldn't decode Identity")
		}

		// Get the given signature
		signature := inst.Invoke.Args.Search("signature")
		if signature == nil {
			return nil, nil, errors.New("Signature args is nil")
		}
		// Update the contract's data with the given signature and identity
		c.deferredData.ProposedTransaction.Instructions[index].SignerIdentities = append(c.deferredData.ProposedTransaction.Instructions[index].SignerIdentities, identity)
		c.deferredData.ProposedTransaction.Instructions[index].Signatures = append(c.deferredData.ProposedTransaction.Instructions[index].Signatures, signature)
		// Save and send the modifications
		cosiDataBuf, err2 := protobuf.Encode(&c.deferredData)
		if err2 != nil {
			return nil, nil, errors.New("Couldn't encode deferredData")
		}
		sc = append(sc, byzcoin.NewStateChange(byzcoin.Update, inst.InstanceID,
			ContractDeferredID, cosiDataBuf, darcID))
		return
	case "execProposedTx":
		// This invocation tries to execute the transaction stored with the
		// "Spawn" invocation. If it is successful, this invocation fills the
		// "ExecResult" field of the "deferredData" struct.

		instructionIDs := make([][]byte, len(c.deferredData.ProposedTransaction.Instructions))

		for i, proposedInstr := range c.deferredData.ProposedTransaction.Instructions {

			// In case it goes well, we want to return the proposed Tx InstanceID
			instructionIDs[i] = proposedInstr.DeriveID("").Slice()

			instructionType := proposedInstr.GetType()

			var contractID string
			switch instructionType {
			case byzcoin.SpawnType:
				contractID = proposedInstr.Spawn.ContractID
			case byzcoin.InvokeType:
				contractID = proposedInstr.Invoke.ContractID
			case byzcoin.DeleteType:
				contractID = proposedInstr.Delete.ContractID
			}

			fn, exists := c.s.GetContractConstructor(contractID)
			if !exists {
				return nil, nil, errors.New("Couldn't get the root function")
			}
			rootInstructionBuff, err := protobuf.Encode(&proposedInstr)
			if err != nil {
				return nil, nil, errors.New("Couldn't encode the root instruction buffer")
			}
			contract, err := fn(rootInstructionBuff)
			if err != nil {
				return nil, nil, errors.New("Couldn't get the root contract")
			}
			err = contract.VerifyDeferredInstruction(rst, proposedInstr, c.deferredData.InstructionHashes[i])
			if err != nil {
				return nil, nil, fmt.Errorf("Verifying the instruction failed: %s", err)
			}

			var stateChanges []byzcoin.StateChange
			switch instructionType {
			case byzcoin.SpawnType:
				stateChanges, _, err = contract.Spawn(rst, proposedInstr, coins)
			case byzcoin.InvokeType:
				stateChanges, _, err = contract.Invoke(rst, proposedInstr, coins)
			case byzcoin.DeleteType:
				stateChanges, _, err = contract.Delete(rst, proposedInstr, coins)

			}

			if err != nil {
				return nil, nil, fmt.Errorf("Error while executing an instruction: %s", err)
			}
			sc = append(sc, stateChanges...)

		}

		c.deferredData.ExecResult = instructionIDs
		// At this stage all verification passed. We can then decrease the
		// NumExecution counter.
		c.deferredData.NumExecution = c.deferredData.NumExecution - 1
		resultBuf, err2 := protobuf.Encode(&c.deferredData)
		if err2 != nil {
			return nil, nil, errors.New("Couldn't encode the result")
		}
		sc = append(sc, byzcoin.NewStateChange(byzcoin.Update, inst.InstanceID,
			ContractDeferredID, resultBuf, darcID))

		return
	default:
		return nil, nil, errors.New("Deferred contract can only addProof and execProposedTx")
	}
}

func (c *contractDeferred) Delete(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, coins []byzcoin.Coin) (sc []byzcoin.StateChange, cout []byzcoin.Coin, err error) {
	cout = coins

	// Find the darcID for this instance.
	var darcID darc.ID
	_, _, _, darcID, err = rst.GetValues(inst.InstanceID.Slice())
	if err != nil {
		return
	}

	sc = byzcoin.StateChanges{
		byzcoin.NewStateChange(byzcoin.Remove, inst.InstanceID, ContractDeferredID, nil, darcID),
	}
	return
}

// VerifyInstruction overrides the basic VerifyInstruction
func (c *contractDeferred) VerifyInstruction(rst byzcoin.ReadOnlyStateTrie, instr byzcoin.Instruction, ctxHash []byte) error {

	// Basic check: can the client actually invoke?
	err := c.BasicContract.VerifyInstruction(rst, instr, ctxHash)
	if err != nil {
		return err
	}

	if instr.GetType() == byzcoin.InvokeType {
		// Global check on the invoke method:
		//   1. The NumExecution should be greater than 0
		//   2. the current skipblock index should be lower than the provided
		//      "expireBlockIndex" argument.

		// 1.
		if c.deferredData.NumExecution < uint64(1) {
			return errors.New("Maximum number of executions reached")
		}

		// 2.
		expireBlockIndex := c.deferredData.ExpireBlockIndex
		currentIndex := uint64(rst.GetIndex())
		if currentIndex > expireBlockIndex {
			return fmt.Errorf("Current block index is too high (%d > %d)", currentIndex, expireBlockIndex)
		}
	}

	if instr.GetType() == byzcoin.InvokeType && instr.Invoke.Command == "addProof" {
		// We will go through 2 checks:
		//   1. Check if the identity is already stored
		//   2. Check if the signature is valid

		// 1:
		// Get the given Identity
		identityBuf := instr.Invoke.Args.Search("identity")
		if identityBuf == nil {
			return errors.New("Identity args is nil")
		}
		identity := darc.Identity{}
		err = protobuf.Decode(identityBuf, &identity)
		if err != nil {
			return errors.New("Couldn't decode Identity")
		}
		// Get the instruction index
		indexBuf := instr.Invoke.Args.Search("index")
		if indexBuf == nil {
			return errors.New("Index args is nil")
		}
		index := binary.LittleEndian.Uint32(indexBuf)

		for _, storedIdentity := range c.deferredData.ProposedTransaction.Instructions[index].SignerIdentities {
			if identity.Equal(&storedIdentity) {
				return errors.New("Identity already stored")
			}
		}
		// 2:
		// Get the given signature
		signature := instr.Invoke.Args.Search("signature")
		if signature == nil {
			return errors.New("Signature args is nil")
		}
		err = identity.Verify(c.InstructionHashes[index], signature)
		if err != nil {
			return errors.New("Bad signature")
		}

		return nil
	}

	return nil
}

// This is a modified version of computing the hash of a transaction. In this
// version, we do not take into account the signers nor the signers counters. We
// also add to the hash the instanceID.
func hashDeferred(instr byzcoin.Instruction, instanceID []byte) []byte {
	h := sha256.New()
	h.Write(instr.InstanceID[:])
	var args []byzcoin.Argument
	switch instr.GetType() {
	case byzcoin.SpawnType:
		h.Write([]byte{0})
		h.Write([]byte(instr.Spawn.ContractID))
		args = instr.Spawn.Args
	case byzcoin.InvokeType:
		h.Write([]byte{1})
		h.Write([]byte(instr.Invoke.ContractID))
		args = instr.Invoke.Args
	case byzcoin.DeleteType:
		h.Write([]byte{2})
		h.Write([]byte(instr.Delete.ContractID))
	}
	for _, a := range args {
		nameBuf := []byte(a.Name)
		nameLenBuf := make([]byte, 8)
		binary.LittleEndian.PutUint64(nameLenBuf, uint64(len(nameBuf)))
		h.Write(nameLenBuf)
		h.Write(nameBuf)

		valueLenBuf := make([]byte, 8)
		binary.LittleEndian.PutUint64(valueLenBuf, uint64(len(a.Value)))
		h.Write(valueLenBuf)
		h.Write(a.Value)
	}
	h.Write(instanceID)

	return h.Sum(nil)
}
