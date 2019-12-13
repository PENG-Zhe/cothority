package calypso

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/dedis/odyssey/catalogc"
	"github.com/dedis/odyssey/projectc"
	"go.dedis.ch/cothority/v3"
	"go.dedis.ch/cothority/v3/byzcoin"
	"go.dedis.ch/cothority/v3/darc"
	"go.dedis.ch/onet/v3"
	"go.dedis.ch/onet/v3/log"
	"go.dedis.ch/onet/v3/network"
	"go.dedis.ch/protobuf"
	"golang.org/x/xerrors"
)

// ContractWriteID references a write contract system-wide.
const ContractWriteID = "calypsoWrite"

// ContractWrite represents one calypso write instance.
type ContractWrite struct {
	byzcoin.BasicContract
	Write
}

// String returns a human readable string representation of the Write data
func (w Write) String() string {
	out := new(strings.Builder)
	out.WriteString("- Write:\n")
	fmt.Fprintf(out, "-- Data: %s\n", w.Data)
	fmt.Fprintf(out, "-- U: %s\n", w.U)
	fmt.Fprintf(out, "-- Ubar: %s\n", w.Ubar)
	fmt.Fprintf(out, "-- E: %s\n", w.E)
	fmt.Fprintf(out, "-- F: %s\n", w.F)
	fmt.Fprintf(out, "-- C: %s\n", w.C)
	fmt.Fprintf(out, "-- ExtraData: %s\n", w.ExtraData)
	fmt.Fprintf(out, "-- LTSID: %s\n", w.LTSID)
	fmt.Fprintf(out, "-- Cost: %x\n", w.Cost)

	return out.String()
}

func contractWriteFromBytes(in []byte) (byzcoin.Contract, error) {
	c := &ContractWrite{}

	err := protobuf.DecodeWithConstructors(in, &c.Write, network.DefaultConstructors(cothority.Suite))
	return c, cothority.ErrorOrNil(err, "couldn't unmarshal write")
}

// Spawn is used to create a new write- or read-contract. The read-contract is
// created by the write-instance, because the creation of a new read-instance is
// protected by the write-contract's darc.
func (c ContractWrite) Spawn(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, coins []byzcoin.Coin) (sc []byzcoin.StateChange, cout []byzcoin.Coin, err error) {
	cout = coins

	var darcID darc.ID
	_, _, _, darcID, err = rst.GetValues(inst.InstanceID.Slice())
	if err != nil {
		err = xerrors.Errorf("getting values: %v", err)
		return
	}

	switch inst.Spawn.ContractID {
	case ContractWriteID:
		w := inst.Spawn.Args.Search("write")
		if w == nil || len(w) == 0 {
			err = xerrors.New("need a write request in 'write' argument")
			return
		}
		err = protobuf.DecodeWithConstructors(w, &c.Write, network.DefaultConstructors(cothority.Suite))
		if err != nil {
			err = xerrors.New("couldn't unmarshal write: " + err.Error())
			return
		}
		if d := inst.Spawn.Args.Search("darcID"); d != nil {
			darcID = d
		}
		if err = c.Write.CheckProof(cothority.Suite, darcID); err != nil {
			err = xerrors.Errorf("proof of write failed: %v", err)
			return
		}
		instID := inst.DeriveID("")
		log.Lvlf3("Successfully verified write request and will store in %x", instID)
		sc = append(sc, byzcoin.NewStateChange(byzcoin.Create, instID, ContractWriteID, w, darcID))
	case ContractReadID:
		var rd Read
		r := inst.Spawn.Args.Search("read")
		if r == nil || len(r) == 0 {
			return nil, nil, xerrors.New("need a read argument")
		}
		err = protobuf.DecodeWithConstructors(r, &rd, network.DefaultConstructors(cothority.Suite))
		if err != nil {
			return nil, nil, xerrors.Errorf("passed read argument is invalid: %v", err)
		}
		if !rd.Write.Equal(inst.InstanceID) {
			return nil, nil, xerrors.New("the read request doesn't reference this write-instance")
		}
		if c.Cost.Value > 0 {
			for i, coin := range cout {
				if coin.Name.Equal(c.Cost.Name) {
					err := coin.SafeSub(c.Cost.Value)
					if err != nil {
						return nil, nil, xerrors.Errorf("couldn't pay for read request: %v", err)
					}
					cout[i] = coin
					break
				}
			}
		}
		sc = byzcoin.StateChanges{byzcoin.NewStateChange(byzcoin.Create, inst.DeriveID(""), ContractReadID, r, darcID)}
	default:
		err = xerrors.New("can only spawn writes and reads")
	}
	return
}

// ContractReadID references a read contract system-wide.
const ContractReadID = "calypsoRead"

// ContractRead represents one read contract.
type ContractRead struct {
	byzcoin.BasicContract
	Read
}

func contractReadFromBytes(in []byte) (byzcoin.Contract, error) {
	return nil, xerrors.New("calypso read instances are never instantiated")
}

// ContractLongTermSecretID is the contract ID for updating the LTS roster.
var ContractLongTermSecretID = "longTermSecret"

type contractLTS struct {
	byzcoin.BasicContract
	LtsInstanceInfo LtsInstanceInfo
}

func contractLTSFromBytes(in []byte) (byzcoin.Contract, error) {
	c := &contractLTS{}

	err := protobuf.DecodeWithConstructors(in, &c.LtsInstanceInfo, network.DefaultConstructors(cothority.Suite))
	return c, cothority.ErrorOrNil(err, "couldn't unmarshal LtsInfo")
}

func (c *contractLTS) Spawn(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, coins []byzcoin.Coin) ([]byzcoin.StateChange, []byzcoin.Coin, error) {
	var darcID darc.ID
	_, _, _, darcID, err := rst.GetValues(inst.InstanceID.Slice())
	if err != nil {
		return nil, nil, xerrors.Errorf("getting values: %v", err)
	}

	if inst.Spawn.ContractID != ContractLongTermSecretID {
		return nil, nil, xerrors.New("can only spawn long-term-secret instances")
	}
	infoBuf := inst.Spawn.Args.Search("lts_instance_info")
	if infoBuf == nil || len(infoBuf) == 0 {
		return nil, nil, xerrors.New("need a lts_instance_info argument")
	}
	var info LtsInstanceInfo
	err = protobuf.DecodeWithConstructors(infoBuf, &info, network.DefaultConstructors(cothority.Suite))
	if err != nil {
		return nil, nil, xerrors.Errorf("passed lts_instance_info argument is invalid: %v", err)
	}
	return byzcoin.StateChanges{byzcoin.NewStateChange(byzcoin.Create, inst.DeriveID(""), ContractLongTermSecretID, infoBuf, darcID)}, coins, nil
}

func (c *contractLTS) Invoke(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, coins []byzcoin.Coin) ([]byzcoin.StateChange, []byzcoin.Coin, error) {
	var darcID darc.ID
	curBuf, _, _, darcID, err := rst.GetValues(inst.InstanceID.Slice())
	if err != nil {
		return nil, nil, xerrors.Errorf("getting values: %v", err)
	}

	if inst.Invoke.Command != "reshare" {
		return nil, nil, xerrors.New("can only reshare long-term secrets")
	}
	infoBuf := inst.Invoke.Args.Search("lts_instance_info")
	if infoBuf == nil || len(infoBuf) == 0 {
		return nil, nil, xerrors.New("need a lts_instance_info argument")
	}

	var curInfo, newInfo LtsInstanceInfo
	err = protobuf.DecodeWithConstructors(infoBuf, &newInfo, network.DefaultConstructors(cothority.Suite))
	if err != nil {
		return nil, nil, xerrors.Errorf("passed lts_instance_info argument is invalid: %v", err)
	}
	err = protobuf.DecodeWithConstructors(curBuf, &curInfo, network.DefaultConstructors(cothority.Suite))
	if err != nil {
		return nil, nil, xerrors.Errorf("current info is invalid: %v", err)
	}

	// Verify the intersection between new roster and the old one. There must be
	// at least a threshold of nodes in the intersection.
	n := len(curInfo.Roster.List)
	overlap := intersectRosters(&curInfo.Roster, &newInfo.Roster)
	thr := n - (n-1)/3
	if overlap < thr {
		return nil, nil, xerrors.New("new roster does not overlap enough with current roster")
	}

	return byzcoin.StateChanges{byzcoin.NewStateChange(byzcoin.Update, inst.InstanceID, ContractLongTermSecretID, infoBuf, darcID)}, coins, nil
}

func intersectRosters(r1, r2 *onet.Roster) int {
	res := 0
	for _, x := range r2.List {
		if i, _ := r1.Search(x.ID); i != -1 {
			res++
		}
	}
	return res
}

// VerifyInstruction uses a specific verification based on attr in the case its
// a read spawn
func (c ContractWrite) VerifyInstruction(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction, ctxHash []byte) error {
	if inst.GetType() == byzcoin.SpawnType && inst.Spawn.ContractID == ContractReadID {
		return inst.VerifyWithOption(rst, ctxHash, &byzcoin.VerificationOptions{EvalAttr: c.MakeAttrInterpreters(rst, inst)})
	}
	return inst.VerifyWithOption(rst, ctxHash, nil)
}

// MakeAttrInterpreters provides the attribute verification which check
// the purposes and uses
func (c ContractWrite) MakeAttrInterpreters(rst byzcoin.ReadOnlyStateTrie, inst byzcoin.Instruction) darc.AttrInterpreters {
	log.Info("Hello from the MakeAttrInterpreters")
	// The allowed rule checks if all the selected attributes by the data
	// scientist are allowed the the data owner. Note that the list of
	// attributes described by the allowed rule contains the attributes of type
	// "allowed" (obviously), but also the attributes of type "must_have". We
	// can therefore see the "must_have" type of attributes as a specialization
	// of the "allowed" one.
	al := func(attr string) error {
		log.Info("Hello from the inside MakeAttrInterpreters")
		// Expecting an 'attr' of form:
		// attribute_id=checked&attribute_id2=hello+world&
		// which, once parsed, gives map[attribute_id:[checked] attribute_id2:[hello+world]]
		parsedQuery, err := url.ParseQuery(attr)
		if err != nil {
			return err
		}

		projectInstID := inst.Spawn.Args.Search("projectInstID")
		if projectInstID == nil {
			return xerrors.New("argument 'projectInstID' not found")
		}

		projectC := projectc.ProjectData{}
		projectBuf, _, _, _, err := rst.GetValues(projectInstID)
		if err != nil {
			return fmt.Errorf("failed to get the given project instance '%x': %s", projectInstID, err.Error())
		}
		err = protobuf.DecodeWithConstructors(projectBuf, &projectC, network.DefaultConstructors(cothority.Suite))
		if err != nil {
			return xerrors.New("failed to decode project instance: " + err.Error())
		}

		// Each attribute selected by the data scientist should be in the
		// attr:allowed list
		var isAllowed func(url.Values, *catalogc.Attribute) error
		isAllowed = func(parsedQuery url.Values, attr *catalogc.Attribute) error {
			log.Info("checking allowed attribute:", attr.ID)
			if attr.Value == "" {
				return nil
			}
			ok := false
			for key, vals := range parsedQuery {
				log.Info("checking key:", key)
				if key != attr.ID {
					continue
				}
				if len(vals) != 1 {
					return xerrors.Errorf("Expected 1 value but got %d. Key: %s, "+
						"vals: %v", len(vals), key, vals)
				}
				val := vals[0]
				if attr.Value != "" && attr.Value != val {
					attr.AddFailedReason(fmt.Sprintf(
						"For dataset '%s': Attribute '%s' must have value '%s', "+
							"but we found value '%s'", inst.InstanceID.String(),
						attr.ID, val, attr.Value))
					// return xerrors.Errorf("Requested an attribute "+
					// 	"with id '%s', we found it but the values don't match. "+
					// 	"Expected '%s', got '%s'", attr.ID, val, attr.Value)
					break
				}
				ok = true
				break
			}
			if !ok {
				attr.AddFailedReason(fmt.Sprintf("For dataset '%s': "+
					"attribute '%s' not allowed", inst.InstanceID.String(), attr.ID))
				// return xerrors.Errorf("attribute '%s' not allowed", attr.ID)
			}
			for _, subAttr := range attr.Attributes {
				if attr.RuleType != "allowed" {
					continue
				}
				isAllowed(parsedQuery, subAttr)
				// if err != nil {
				// 	return xerrors.Errorf("attribute '%s' not allowed", subAttr.ID)
				// }
			}
			return nil
		}

		for _, ag := range projectC.Metadata.AttributesGroups {
			for _, attr := range ag.Attributes {
				// The "must_have" attributes must be checked by the other rule,
				// because the user can actually check more "must_have"
				// attributes that are required.
				if attr.RuleType != "allowed" {
					continue
				}
				isAllowed(parsedQuery, attr)
				// if err != nil {
				// 	return xerrors.Errorf("failed to check an allowed attribute: %v", err)
				// }
			}
		}

		failedReasons := projectC.Metadata.FailedReasons()
		if failedReasons != "" {
			return xerrors.Errorf("attr:allowed verification failed, you can "+
				"check the failedReasons field of each attribute to learn more. "+
				"Here is a summary:\n%s", failedReasons)
		}

		return nil
	}

	// Here we check if the specified "must have" attributes that the data owner
	// set appear in the selected attributes from the data scientist.
	mh := func(attr string) error {
		log.Info("Hello from the inside MakeAttrInterpreters")
		// Expecting an 'attr' of form:
		// attribute_id=checked&attribute_id2=hello+world&
		// which, once parsed, gives map[attribute_id:[checked] attribute_id2:[hello+world]]
		parsedQuery, err := url.ParseQuery(attr)
		if err != nil {
			return err
		}

		projectInstID := inst.Spawn.Args.Search("projectInstID")
		if projectInstID == nil {
			return xerrors.New("argument 'projectInstID' not found")
		}

		projectC := projectc.ProjectData{}
		projectBuf, _, _, _, err := rst.GetValues(projectInstID)
		if err != nil {
			return fmt.Errorf("failed to get the given project instance '%x': %s", projectInstID, err.Error())
		}
		err = protobuf.DecodeWithConstructors(projectBuf, &projectC, network.DefaultConstructors(cothority.Suite))
		if err != nil {
			return xerrors.Errorf("failed to decode project instance: %v", err)
		}

		// Each attribute should have a corresponding Metadata.Attribute that
		// has a corresponding value.
		for key, vals := range parsedQuery {
			if len(vals) != 1 {
				return xerrors.Errorf("Expected 1 value but got %d. Key: %s, "+
					"vals: %v", len(vals), key, vals)
			}
			val := vals[0]
			attr, found := projectC.Metadata.GetAttribute(key)
			if !found {
				return xerrors.Errorf("Must have attribute with key '%s' not found", key)
			}
			if val != "" && attr.Value != val {
				return xerrors.Errorf("Must have attribute with key '%s' does not have "+
					"a matching value. Expected '%s', got '%s'", key, val, attr.Value)
			}
		}
		return nil
	}
	return darc.AttrInterpreters{"must_have": mh, "allowed": al}
}

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
