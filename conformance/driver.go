package conformance

import (
	"context"
	gobig "math/big"
	"os"

	"github.com/EpiK-Protocol/go-epik/chain/state"
	"github.com/EpiK-Protocol/go-epik/chain/stmgr"
	"github.com/EpiK-Protocol/go-epik/chain/store"
	"github.com/EpiK-Protocol/go-epik/chain/types"
	"github.com/EpiK-Protocol/go-epik/chain/vm"
	"github.com/EpiK-Protocol/go-epik/conformance/chaos"
	"github.com/EpiK-Protocol/go-epik/extern/sector-storage/ffiwrapper"
	"github.com/EpiK-Protocol/go-epik/lib/blockstore"

	_ "github.com/EpiK-Protocol/go-epik/lib/sigs/bls"  // enable bls signatures
	_ "github.com/EpiK-Protocol/go-epik/lib/sigs/secp" // enable secp signatures

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/crypto"

	"github.com/filecoin-project/test-vectors/schema"

	"github.com/filecoin-project/go-address"

	"github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
)

var (
	// DefaultCirculatingSupply is the fallback circulating supply returned by
	// the driver's CircSupplyCalculator function, used if the vector specifies
	// no circulating supply.
	DefaultCirculatingSupply = types.TotalFilecoinInt

	// DefaultBaseFee to use in the VM, if one is not supplied in the vector.
	DefaultBaseFee = abi.NewTokenAmount(100)
)

type Driver struct {
	ctx      context.Context
	selector schema.Selector
	vmFlush  bool
}

type DriverOpts struct {
	// DisableVMFlush, when true, avoids calling VM.Flush(), forces a blockstore
	// recursive copy, from the temporary buffer blockstore, to the real
	// system's blockstore. Disabling VM flushing is useful when extracting test
	// vectors and trimming state, as we don't want to force an accidental
	// deep copy of the state tree.
	//
	// Disabling VM flushing almost always should go hand-in-hand with
	// EPIK_DISABLE_VM_BUF=iknowitsabadidea. That way, state tree writes are
	// immediately committed to the blockstore.
	DisableVMFlush bool
}

func NewDriver(ctx context.Context, selector schema.Selector, opts DriverOpts) *Driver {
	return &Driver{ctx: ctx, selector: selector, vmFlush: !opts.DisableVMFlush}
}

type ExecuteTipsetResult struct {
	ReceiptsRoot  cid.Cid
	PostStateRoot cid.Cid

	// AppliedMessages stores the messages that were applied, in the order they
	// were applied. It includes implicit messages (cron, rewards).
	AppliedMessages []*types.Message
	// AppliedResults stores the results of AppliedMessages, in the same order.
	AppliedResults []*vm.ApplyRet
}

// ExecuteTipset executes the supplied tipset on top of the state represented
// by the preroot CID.
//
// parentEpoch is the last epoch in which an actual tipset was processed. This
// is used by Epik for null block counting and cron firing.
//
// This method returns the the receipts root, the poststate root, and the VM
// message results. The latter _include_ implicit messages, such as cron ticks
// and reward withdrawal per miner.
func (d *Driver) ExecuteTipset(bs blockstore.Blockstore, ds ds.Batching, preroot cid.Cid, parentEpoch abi.ChainEpoch, tipset *schema.Tipset, execEpoch abi.ChainEpoch) (*ExecuteTipsetResult, error) {
	var (
		syscalls = vm.Syscalls(ffiwrapper.ProofVerifier)
		vmRand   = NewFixedRand()

		cs = store.NewChainStore(bs, bs, ds, syscalls, nil)
		sm = stmgr.NewStateManager(cs)
	)

	blocks := make([]store.BlockMessages, 0, len(tipset.Blocks))
	for _, b := range tipset.Blocks {
		sb := store.BlockMessages{
			Miner:    b.MinerAddr,
			WinCount: b.WinCount,
		}
		for _, m := range b.Messages {
			msg, err := types.DecodeMessage(m)
			if err != nil {
				return nil, err
			}
			switch msg.From.Protocol() {
			case address.SECP256K1:
				sb.SecpkMessages = append(sb.SecpkMessages, toChainMsg(msg))
			case address.BLS:
				sb.BlsMessages = append(sb.BlsMessages, toChainMsg(msg))
			default:
				// sneak in messages originating from other addresses as both kinds.
				// these should fail, as they are actually invalid senders.
				sb.SecpkMessages = append(sb.SecpkMessages, msg)
				sb.BlsMessages = append(sb.BlsMessages, msg)
			}
		}
		blocks = append(blocks, sb)
	}

	var (
		messages []*types.Message
		results  []*vm.ApplyRet

		basefee = abi.NewTokenAmount(tipset.BaseFee.Int64())
	)

	postcid, receiptsroot, err := sm.ApplyBlocks(context.Background(), parentEpoch, preroot, blocks, execEpoch, vmRand, func(_ cid.Cid, msg *types.Message, ret *vm.ApplyRet) error {
		messages = append(messages, msg)
		results = append(results, ret)
		return nil
	}, basefee, nil)

	if err != nil {
		return nil, err
	}

	ret := &ExecuteTipsetResult{
		ReceiptsRoot:    receiptsroot,
		PostStateRoot:   postcid,
		AppliedMessages: messages,
		AppliedResults:  results,
	}
	return ret, nil
}

type ExecuteMessageParams struct {
	Preroot    cid.Cid
	Epoch      abi.ChainEpoch
	Message    *types.Message
	CircSupply abi.TokenAmount
	BaseFee    abi.TokenAmount

	// Rand is an optional vm.Rand implementation to use. If nil, the driver
	// will use a vm.Rand that returns a fixed value for all calls.
	Rand vm.Rand
}

// ExecuteMessage executes a conformance test vector message in a temporary VM.
func (d *Driver) ExecuteMessage(bs blockstore.Blockstore, params ExecuteMessageParams) (*vm.ApplyRet, cid.Cid, error) {
	if !d.vmFlush {
		// do not flush the VM, just the state tree; this should be used with
		// EPIK_DISABLE_VM_BUF enabled, so writes will anyway be visible.
		_ = os.Setenv("EPIK_DISABLE_VM_BUF", "iknowitsabadidea")
	}

	if params.Rand == nil {
		params.Rand = NewFixedRand()
	}

	// dummy state manager; only to reference the GetNetworkVersion method,
	// which does not depend on state.
	sm := stmgr.NewStateManager(nil)

	vmOpts := &vm.VMOpts{
		StateBase: params.Preroot,
		Epoch:     params.Epoch,
		Bstore:    bs,
		Syscalls:  vm.Syscalls(ffiwrapper.ProofVerifier),
		CircSupplyCalc: func(_ context.Context, _ abi.ChainEpoch, _ *state.StateTree) (abi.TokenAmount, error) {
			return params.CircSupply, nil
		},
		Rand:        params.Rand,
		BaseFee:     params.BaseFee,
		NtwkVersion: sm.GetNtwkVersion,
	}

	lvm, err := vm.NewVM(context.TODO(), vmOpts)
	if err != nil {
		return nil, cid.Undef, err
	}

	invoker := vm.NewActorRegistry()

	// register the chaos actor if required by the vector.
	if chaosOn, ok := d.selector["chaos_actor"]; ok && chaosOn == "true" {
		invoker.Register(nil, chaos.Actor{})
	}

	lvm.SetInvoker(invoker)

	ret, err := lvm.ApplyMessage(d.ctx, toChainMsg(params.Message))
	if err != nil {
		return nil, cid.Undef, err
	}

	var root cid.Cid
	if d.vmFlush {
		// flush the VM, committing the state tree changes and forcing a
		// recursive copoy from the temporary blcokstore to the real blockstore.
		root, err = lvm.Flush(d.ctx)
	} else {
		root, err = lvm.StateTree().(*state.StateTree).Flush(d.ctx)
	}

	return ret, root, err
}

// toChainMsg injects a synthetic 0-filled signature of the right length to
// messages that originate from secp256k senders, leaving all
// others untouched.
// TODO: generate a signature in the DSL so that it's encoded in
//  the test vector.
func toChainMsg(msg *types.Message) (ret types.ChainMsg) {
	ret = msg
	if msg.From.Protocol() == address.SECP256K1 {
		ret = &types.SignedMessage{
			Message: *msg,
			Signature: crypto.Signature{
				Type: crypto.SigTypeSecp256k1,
				Data: make([]byte, 65),
			},
		}
	}
	return ret
}

// BaseFeeOrDefault converts a basefee as passed in a test vector (go *big.Int
// type) to an abi.TokenAmount, or if nil it returns the DefaultBaseFee.
func BaseFeeOrDefault(basefee *gobig.Int) abi.TokenAmount {
	if basefee == nil {
		return DefaultBaseFee
	}
	return big.NewFromGo(basefee)
}

// CircSupplyOrDefault converts a circulating supply as passed in a test vector
// (go *big.Int type) to an abi.TokenAmount, or if nil it returns the
// DefaultCirculatingSupply.
func CircSupplyOrDefault(circSupply *gobig.Int) abi.TokenAmount {
	if circSupply == nil {
		return DefaultCirculatingSupply
	}
	return big.NewFromGo(circSupply)
}
