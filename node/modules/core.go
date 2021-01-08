package modules

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/ioutil"
	"time"

	"github.com/gbrlsnchs/jwt/v3"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	record "github.com/libp2p/go-libp2p-record"
	"go.uber.org/fx"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-jsonrpc/auth"
	"github.com/filecoin-project/go-state-types/abi"

	"github.com/EpiK-Protocol/go-epik/api/apistruct"
	"github.com/EpiK-Protocol/go-epik/build"
	"github.com/EpiK-Protocol/go-epik/chain/types"
	"github.com/EpiK-Protocol/go-epik/lib/addrutil"
	"github.com/EpiK-Protocol/go-epik/node/config"
	"github.com/EpiK-Protocol/go-epik/node/modules/dtypes"
	"github.com/EpiK-Protocol/go-epik/node/repo"
	"github.com/EpiK-Protocol/go-epik/system"
	"github.com/raulk/go-watchdog"
)

const (
	JWTSecretName   = "auth-jwt-private" //nolint:gosec
	KTJwtHmacSecret = "jwt-hmac-secret"  //nolint:gosec
)

var (
	log         = logging.Logger("modules")
	logWatchdog = logging.Logger("watchdog")
)

type Genesis func() (*types.BlockHeader, error)

// RecordValidator provides namesys compatible routing record validator
func RecordValidator(ps peerstore.Peerstore) record.Validator {
	return record.NamespacedValidator{
		"pk": record.PublicKeyValidator{},
	}
}

// MemoryWatchdog starts the memory watchdog, applying the computed resource
// constraints.
func MemoryWatchdog(lc fx.Lifecycle) {
	cfg := watchdog.MemConfig{
		Resolution: 10 * time.Second,
		Policy: &watchdog.WatermarkPolicy{
			Watermarks:         []float64{0.50, 0.60, 0.70, 0.85, 0.90, 0.925, 0.95},
			EmergencyWatermark: 0.95,
			Silence:            45 * time.Second, // avoid forced GC within 45 seconds of previous GC.
		},
		Logger: logWatchdog,
	}

	// if user has set max heap limit, apply it. Otherwise, fall back to total
	// system memory constraint.
	if maxHeap := system.ResourceConstraints.MaxHeapMem; maxHeap != 0 {
		log.Infof("memory watchdog will apply max heap constraint: %d bytes", maxHeap)
		cfg.Limit = maxHeap
		cfg.Scope = watchdog.ScopeHeap
	} else {
		log.Infof("max heap size not provided; memory watchdog will apply total system memory constraint: %d bytes", system.ResourceConstraints.TotalSystemMem)
		cfg.Limit = system.ResourceConstraints.TotalSystemMem
		cfg.Scope = watchdog.ScopeSystem
	}

	err, stop := watchdog.Memory(cfg)
	if err != nil {
		log.Warnf("failed to instantiate memory watchdog: %s", err)
		return
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			stop()
			return nil
		},
	})
}

type JwtPayload struct {
	Allow []auth.Permission
}

func APISecret(keystore types.KeyStore, lr repo.LockedRepo) (*dtypes.APIAlg, error) {
	key, err := keystore.Get(JWTSecretName)

	if errors.Is(err, types.ErrKeyInfoNotFound) {
		log.Warn("Generating new API secret")

		sk, err := ioutil.ReadAll(io.LimitReader(rand.Reader, 32))
		if err != nil {
			return nil, err
		}

		key = types.KeyInfo{
			Type:       KTJwtHmacSecret,
			PrivateKey: sk,
		}

		if err := keystore.Put(JWTSecretName, key); err != nil {
			return nil, xerrors.Errorf("writing API secret: %w", err)
		}

		// TODO: make this configurable
		p := JwtPayload{
			Allow: apistruct.AllPermissions,
		}

		cliToken, err := jwt.Sign(&p, jwt.NewHS256(key.PrivateKey))
		if err != nil {
			return nil, err
		}

		if err := lr.SetAPIToken(cliToken); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, xerrors.Errorf("could not get JWT Token: %w", err)
	}

	return (*dtypes.APIAlg)(jwt.NewHS256(key.PrivateKey)), nil
}

func ConfigBootstrap(peers []string) func() (dtypes.BootstrapPeers, error) {
	return func() (dtypes.BootstrapPeers, error) {
		return addrutil.ParseAddresses(context.TODO(), peers)
	}
}

func BuiltinBootstrap() (dtypes.BootstrapPeers, error) {
	return build.BuiltinBootstrap()
}

func DrandBootstrap(ds dtypes.DrandSchedule) (dtypes.DrandBootstrap, error) {
	// TODO: retry resolving, don't fail if at least one resolve succeeds
	res := []peer.AddrInfo{}
	for _, d := range ds {
		addrs, err := addrutil.ParseAddresses(context.TODO(), d.Config.Relays)
		if err != nil {
			log.Errorf("reoslving drand relays addresses: %+v", err)
			continue
		}
		res = append(res, addrs...)
	}
	return res, nil
}

func NewDefaultMaxFeeFunc(r repo.LockedRepo) dtypes.DefaultMaxFeeFunc {
	return func() (out abi.TokenAmount, err error) {
		err = readNodeCfg(r, func(cfg *config.FullNode) {
			out = abi.TokenAmount(cfg.Fees.DefaultMaxFee)
		})
		return
	}
}

func readNodeCfg(r repo.LockedRepo, accessor func(node *config.FullNode)) error {
	raw, err := r.Config()
	if err != nil {
		return err
	}

	cfg, ok := raw.(*config.FullNode)
	if !ok {
		return xerrors.New("expected config.FullNode")
	}

	accessor(cfg)

	return nil
}
