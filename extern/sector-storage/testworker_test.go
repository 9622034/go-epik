package sectorstorage

import (
	"context"
	"sync"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-storage/storage"
	"github.com/google/uuid"

	"github.com/EpiK-Protocol/go-epik/extern/sector-storage/mock"
	"github.com/EpiK-Protocol/go-epik/extern/sector-storage/sealtasks"
	"github.com/EpiK-Protocol/go-epik/extern/sector-storage/stores"
	"github.com/EpiK-Protocol/go-epik/extern/sector-storage/storiface"
)

type testWorker struct {
	acceptTasks map[sealtasks.TaskType]struct{}
	lstor       *stores.Local
	ret         storiface.WorkerReturn

	mockSeal *mock.SectorMgr

	pc1s    int
	pc1lk   sync.Mutex
	pc1wait *sync.WaitGroup

	session uuid.UUID

	Worker
}

func newTestWorker(wcfg WorkerConfig, lstor *stores.Local, ret storiface.WorkerReturn) *testWorker {
	ssize, err := wcfg.SealProof.SectorSize()
	if err != nil {
		panic(err)
	}

	acceptTasks := map[sealtasks.TaskType]struct{}{}
	for _, taskType := range wcfg.TaskTypes {
		acceptTasks[taskType] = struct{}{}
	}

	return &testWorker{
		acceptTasks: acceptTasks,
		lstor:       lstor,
		ret:         ret,

		mockSeal: mock.NewMockSectorMgr(ssize, nil),

		session: uuid.New(),
	}
}

func (t *testWorker) asyncCall(sector abi.SectorID, work func(ci storiface.CallID)) (storiface.CallID, error) {
	ci := storiface.CallID{
		Sector: sector,
		ID:     uuid.New(),
	}

	go work(ci)

	return ci, nil
}

func (t *testWorker) AddPiece(ctx context.Context, sector abi.SectorID, pieceSizes []abi.UnpaddedPieceSize, newPieceSize abi.UnpaddedPieceSize, pieceData storage.Data) (storiface.CallID, error) {
	return t.asyncCall(sector, func(ci storiface.CallID) {
		p, err := t.mockSeal.AddPiece(ctx, sector, pieceSizes, newPieceSize, pieceData)
		if err := t.ret.ReturnAddPiece(ctx, ci, p, errstr(err)); err != nil {
			log.Error(err)
		}
	})
}

func (t *testWorker) SealPreCommit1(ctx context.Context, sector abi.SectorID, ticket abi.SealRandomness, pieces []abi.PieceInfo) (storiface.CallID, error) {
	return t.asyncCall(sector, func(ci storiface.CallID) {
		t.pc1s++

		if t.pc1wait != nil {
			t.pc1wait.Done()
		}

		t.pc1lk.Lock()
		defer t.pc1lk.Unlock()

		p1o, err := t.mockSeal.SealPreCommit1(ctx, sector, ticket, pieces)
		if err := t.ret.ReturnSealPreCommit1(ctx, ci, p1o, errstr(err)); err != nil {
			log.Error(err)
		}
	})
}

func (t *testWorker) Fetch(ctx context.Context, sector abi.SectorID, fileType storiface.SectorFileType, ptype storiface.PathType, am storiface.AcquireMode) (storiface.CallID, error) {
	return t.asyncCall(sector, func(ci storiface.CallID) {
		if err := t.ret.ReturnFetch(ctx, ci, ""); err != nil {
			log.Error(err)
		}
	})
}

func (t *testWorker) TaskTypes(ctx context.Context) (map[sealtasks.TaskType]struct{}, error) {
	return t.acceptTasks, nil
}

func (t *testWorker) Paths(ctx context.Context) ([]stores.StoragePath, error) {
	return t.lstor.Local(ctx)
}

func (t *testWorker) Info(ctx context.Context) (storiface.WorkerInfo, error) {
	res := ResourceTable[sealtasks.TTPreCommit2][abi.RegisteredSealProof_StackedDrg2KiBV1]

	return storiface.WorkerInfo{
		Hostname: "testworkerer",
		Resources: storiface.WorkerResources{
			MemPhysical: res.MinMemory * 3,
			MemSwap:     0,
			MemReserved: res.MinMemory,
			CPUs:        32,
			GPUs:        nil,
		},
	}, nil
}

func (t *testWorker) Session(context.Context) (uuid.UUID, error) {
	return t.session, nil
}

func (t *testWorker) Close() error {
	panic("implement me")
}

var _ Worker = &testWorker{}
