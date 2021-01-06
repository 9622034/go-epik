// +build !debug
// +build !2k
// +build !testground

package build

import (
	"os"

	"github.com/EpiK-Protocol/go-epik/chain/actors/policy"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	builtin2 "github.com/filecoin-project/specs-actors/v2/actors/builtin"
)

var DrandSchedule = map[abi.ChainEpoch]DrandEnum{
	0: DrandMainnet,
}

// const UpgradeBreezeHeight = 41280
// const BreezeGasTampingDuration = 120

// const UpgradeSmokeHeight = 51000

// const UpgradeIgnitionHeight = 94000
// const UpgradeRefuelHeight = 130800

// var UpgradeActorsV2Height = abi.ChainEpoch(138720)

// const UpgradeTapeHeight = 140760

// This signals our tentative epoch for mainnet launch. Can make it later, but not earlier.
// Miners, clients, developers, custodians all need time to prepare.
// We still have upgrades and state changes to do, but can happen after signaling timing here.
// const UpgradeLiftoffHeight = 148888

// const UpgradeKumquatHeight = 170000

func init() {
	policy.SetConsensusMinerMinPower(abi.NewStoragePower(1))
	policy.SetSupportedProofTypes(
		abi.RegisteredSealProof_StackedDrg8MiBV1_1,
	)

	if os.Getenv("EPIK_USE_TEST_ADDRESSES") != "1" {
		SetAddressNetwork(address.Mainnet)
	}

	Devnet = false
}

const BlockDelaySecs = uint64(builtin2.EpochDurationSeconds)

const PropagationDelaySecs = uint64(6)

const BootstrapPeerThreshold = 4
