package retrievaladapter

import (
	"context"
	"io"

	"github.com/EpiK-Protocol/go-epik/api"
	"github.com/EpiK-Protocol/go-epik/chain/actors"
	"github.com/EpiK-Protocol/go-epik/chain/actors/builtin/paych"
	"github.com/EpiK-Protocol/go-epik/chain/actors/builtin/retrieval"
	"github.com/EpiK-Protocol/go-epik/chain/types"
	sectorstorage "github.com/EpiK-Protocol/go-epik/extern/sector-storage"
	"github.com/EpiK-Protocol/go-epik/extern/sector-storage/storiface"
	"github.com/EpiK-Protocol/go-epik/storage"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/shared"
	"github.com/filecoin-project/go-state-types/abi"
	specstorage "github.com/filecoin-project/specs-storage/storage"

	"github.com/ipfs/go-cid"
)

type retrievalProviderNode struct {
	miner  *storage.Miner
	sealer sectorstorage.SectorManager
	full   api.FullNode
}

// NewRetrievalProviderNode returns a new node adapter for a retrieval provider that talks to the
// epik Node
func NewRetrievalProviderNode(miner *storage.Miner, sealer sectorstorage.SectorManager, full api.FullNode) retrievalmarket.RetrievalProviderNode {
	return &retrievalProviderNode{miner, sealer, full}
}

func (rpn *retrievalProviderNode) GetMinerWorkerAddress(ctx context.Context, miner address.Address, tok shared.TipSetToken) (address.Address, error) {
	tsk, err := types.TipSetKeyFromBytes(tok)
	if err != nil {
		return address.Undef, err
	}

	mi, err := rpn.full.StateMinerInfo(ctx, miner, tsk)
	return mi.Worker, err
}

func (rpn *retrievalProviderNode) UnsealSector(ctx context.Context, sectorID abi.SectorNumber, offset abi.UnpaddedPieceSize, length abi.UnpaddedPieceSize) (io.ReadCloser, error) {
	si, err := rpn.miner.GetSectorInfo(sectorID)
	if err != nil {
		return nil, err
	}

	mid, err := address.IDFromAddress(rpn.miner.Address())
	if err != nil {
		return nil, err
	}

	ref := specstorage.SectorRef{
		ID: abi.SectorID{
			Miner:  abi.ActorID(mid),
			Number: sectorID,
		},
		ProofType: si.SectorType,
	}

	r, w := io.Pipe()
	go func() {
		var commD cid.Cid
		if si.CommD != nil {
			commD = *si.CommD
		}
		err := rpn.sealer.ReadPiece(ctx, w, ref, storiface.UnpaddedByteIndex(offset), length, si.TicketValue, commD)
		_ = w.CloseWithError(err)
	}()

	return r, nil
}

func (rpn *retrievalProviderNode) SavePaymentVoucher(ctx context.Context, paymentChannel address.Address, voucher *paych.SignedVoucher, proof []byte, expectedAmount abi.TokenAmount, tok shared.TipSetToken) (abi.TokenAmount, error) {
	// TODO: respect the provided TipSetToken (a serialized TipSetKey) when
	// querying the chain
	// added, err := rpn.full.PaychVoucherAdd(ctx, paymentChannel, voucher, proof, expectedAmount)
	// return added, err
	return expectedAmount, nil
}

func (rpn *retrievalProviderNode) ConfirmComplete(ctx context.Context, pieceCid cid.Cid, size uint64) (cid.Cid, error) {
	params, aerr := actors.SerializeParams(&retrieval.RetrievalData{
		PieceID:  pieceCid,
		Size:     size,
		Provider: rpn.miner.Address(),
	})
	if aerr != nil {
		return cid.Undef, aerr
	}

	addr, err := rpn.full.WalletDefaultAddress(ctx)
	if err != nil {
		return cid.Undef, err
	}

	msg := types.Message{
		To:     retrieval.Address,
		From:   addr,
		Value:  abi.NewTokenAmount(0),
		Method: retrieval.Methods.ConfirmData,
		Params: params,
	}
	sm, err := rpn.full.MpoolPushMessage(ctx, &msg, nil)
	if err != nil {
		return cid.Undef, err
	}
	return sm.Cid(), nil
}

func (rpn *retrievalProviderNode) GetChainHead(ctx context.Context) (shared.TipSetToken, abi.ChainEpoch, error) {
	head, err := rpn.full.ChainHead(ctx)
	if err != nil {
		return nil, 0, err
	}

	return head.Key().Bytes(), head.Height(), nil
}
