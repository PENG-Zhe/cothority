package jvss

import (
	"errors"
	"fmt"
	"time"

	"github.com/dedis/cothority/lib/dbg"
	"github.com/dedis/cothority/lib/sda"
	"github.com/dedis/crypto/abstract"
	"github.com/dedis/crypto/config"
	"github.com/dedis/crypto/poly"
)

func init() {
	sda.ProtocolRegisterName("JVSS", NewJVSS)
}

// JVSS is the main protocol struct and implements the sda.ProtocolInstance
// interface.
type JVSS struct {
	*sda.Node                           // The SDA TreeNode
	keyPair      *config.KeyPair        // KeyPair of the host
	nodeList     []*sda.TreeNode        // List of TreeNodes in the JVSS group
	pubKeys      []abstract.Point       // List of public keys of the above TreeNodes
	info         poly.Threshold         // JVSS thresholds
	schnorr      *poly.Schnorr          // Long-term Schnorr struct to compute distributed signatures
	lts          string                 // ID of the long-term shared secret
	secrets      map[string]*JVSSSecret // Shared secrets (long- and short-term ones)
	ltSecretInit bool                   // Indicator whether shared secret has been initialised or not
	SetupDone    chan bool              // Channel to indicate when JVSS setup is done
	sigChan      chan *poly.SchnorrSig  // Channel for JVSS signature
}

// JVSSSecret contains all information for long- and short-term (i.e. random)
// shared secrets
type JVSSSecret struct {
	secret   *poly.SharedSecret // Shared secret
	receiver *poly.Receiver     // Receiver to aggregate deals
	numDeals int                // Number of collected deals in the receiver
	dealInit bool               // Indicator whether own deal has been initialised and broadcasted or not
	numPSigs int                // Number of collected partial signatures
}

// NewJVSS creates a new JVSS protocol instance and returns it.
func NewJVSS(node *sda.Node) (sda.ProtocolInstance, error) {

	kp := &config.KeyPair{Suite: node.Suite(), Public: node.Public(), Secret: node.Private()}
	nodes := node.Tree().ListNodes()
	pk := make([]abstract.Point, len(nodes))
	for i, tn := range nodes {
		pk[i] = tn.Entity.Public
	}
	// Note: T <= R <= N (for simplicity we use T = R = N; might change later)
	info := poly.Threshold{T: len(nodes), R: len(nodes), N: len(nodes)}

	jv := &JVSS{
		Node:         node,
		keyPair:      kp,
		nodeList:     nodes,
		pubKeys:      pk,
		info:         info,
		schnorr:      new(poly.Schnorr),
		lts:          "LTSS",
		secrets:      make(map[string]*JVSSSecret),
		ltSecretInit: false,
		SetupDone:    make(chan bool, 1),
		sigChan:      make(chan *poly.SchnorrSig),
	}

	// Setup message handlers
	handlers := []interface{}{
		jv.handleSetup,
		jv.handleSigReq,
		jv.handleSigResp,
	}
	for _, h := range handlers {
		if err := jv.RegisterHandler(h); err != nil {
			return nil, errors.New("Could not register handler: " + err.Error())
		}
	}

	return jv, nil
}

// Start initiates the JVSS protocol by setting up a long-term shared secret
// which can be used later on by the JVSS group to sign and verify messages.
func (jv *JVSS) Start() error {
	jv.initSecret(jv.lts)
	time.Sleep(1 * time.Second) // TODO: workaround

	jv.SetupDone <- true
	return nil
}

// Verify
func (jv *JVSS) Verify(msg []byte, sig *poly.SchnorrSig) error {
	h := jv.keyPair.Suite.Hash()
	h.Write(msg)
	return jv.schnorr.VerifySchnorrSig(sig, h)
}

// Sign
func (jv *JVSS) Sign(msg []byte) (*poly.SchnorrSig, error) {

	// initialise short-term shared secret only used for this signing request
	sid := fmt.Sprintf("STSS%d", jv.nodeIdx())
	jv.initSecret(sid)
	time.Sleep(1 * time.Second) // TODO: another workaround, replace by a channel or so

	ps := jv.sigPartial(sid, msg)
	if err := jv.schnorr.AddPartialSig(ps); err != nil {
		return nil, err
	}
	sts := jv.secrets[sid]
	sts.numPSigs++

	// broadcast signing request (see line 212)
	req := &SigReqMsg{
		Src: jv.nodeIdx(),
		SID: sid,
		Msg: msg,
	}
	jv.broadcast(req)

	// wait for signature
	sig := <-jv.sigChan

	return sig, nil
}

func (jv *JVSS) initSecret(sid string) {

	// Initialise shared secret of given type if necessary
	if _, ok := jv.secrets[sid]; !ok {
		dbg.Lvl1("Initialising shared secret", sid)
		sec := &JVSSSecret{
			receiver: poly.NewReceiver(jv.keyPair.Suite, jv.info, jv.keyPair),
			numDeals: 0,
			dealInit: false,
			numPSigs: 0,
		}
		jv.secrets[sid] = sec
	}

	secret := jv.secrets[sid]

	// Initialise and broadcast our deal if necessary
	if !secret.dealInit {
		secret.dealInit = true
		kp := config.NewKeyPair(jv.keyPair.Suite)
		deal := new(poly.Deal).ConstructDeal(kp, jv.keyPair, jv.info.T, jv.info.R, jv.pubKeys)
		jv.addDeal(sid, deal)
		db, _ := deal.MarshalBinary()
		jv.broadcast(&SetupMsg{Src: jv.nodeIdx(), SID: sid, Deal: db})
	}
}

func (jv *JVSS) addDeal(sid string, deal *poly.Deal) {
	secret, ok := jv.secrets[sid]
	if !ok {
		dbg.Errorf("Error shared secret does not exist")
	}
	if _, err := secret.receiver.AddDeal(jv.nodeIdx(), deal); err != nil {
		dbg.Errorf("Error adding deal to receiver %d: %v", jv.nodeIdx(), err)
	}
	secret.numDeals += 1
	dbg.Lvl1(fmt.Sprintf("Node %d: deals %d/%d", jv.nodeIdx(), secret.numDeals, len(jv.nodeList)))
}

func (jv *JVSS) finaliseSecret(sid string) {
	secret := jv.secrets[sid]
	if secret.numDeals == jv.info.T {
		sec, err := secret.receiver.ProduceSharedSecret()
		if err != nil {
			dbg.Errorf("Error node %d could not create shared secret %s: %v", jv.nodeIdx(), sid, err)
		}
		secret.secret = sec

		dbg.Lvl1(fmt.Sprintf("Node %d: shared secret %s created", jv.nodeIdx(), sid))

		// Initialise long-term shared secret if not done so before
		if !jv.ltSecretInit && sid == jv.lts {
			jv.ltSecretInit = true
			jv.schnorr.Init(jv.keyPair.Suite, jv.info, secret.secret)
			dbg.Lvl1(fmt.Sprintf("Node %d: Schnorr struct for shared secret %s initialised", jv.nodeIdx(), sid))
		}
	}
}

func (jv *JVSS) sigPartial(sid string, msg []byte) *poly.SchnorrPartialSig {
	secret := jv.secrets[sid]
	hash := jv.keyPair.Suite.Hash()
	hash.Write(msg)
	if err := jv.schnorr.NewRound(secret.secret, hash); err != nil {
		dbg.Errorf("Error node %d could not start new signing round: %v", jv.nodeIdx(), err)
		return nil
	}
	ps := jv.schnorr.RevealPartialSig()
	if ps == nil {
		dbg.Errorf("Error node %d could not create partial signature", jv.nodeIdx())
	}
	return ps
}

func (jv *JVSS) nodeIdx() int {
	return jv.Node.TreeNode().EntityIdx
}

func (jv *JVSS) broadcast(msg interface{}) {
	for idx, node := range jv.nodeList {
		if idx != jv.nodeIdx() {
			if err := jv.SendTo(node, msg); err != nil {
				dbg.Errorf("Error sending msg to node %d: %v", idx, err)
			}
		}
	}
}
