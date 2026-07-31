package bitcoin

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"
)

func TestGetOrCreateAddressConcurrentSameUserReturnsOneActiveAddress(t *testing.T) {
	svc, store, _ := newHardeningTestService(t)
	const n = 50
	results := make([]*BTCAddress, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.GetOrCreateAddress(context.Background(), "user-concurrent")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}
	want := results[0].Address
	for i, got := range results {
		if got == nil || got.Address != want {
			t.Fatalf("request %d returned %v, want address %s", i, got, want)
		}
	}
	active := store.activeAddressesFor("user-concurrent", string(Regtest))
	if len(active) != 1 {
		t.Fatalf("want one active BTC address, got %d: %+v", len(active), active)
	}
	if active[0].Address != want {
		t.Fatalf("returned address %s differs from persisted active %s", want, active[0].Address)
	}
}

func TestGetOrCreateAddressConcurrentTwoUsersDifferentOwnership(t *testing.T) {
	svc, store, _ := newHardeningTestService(t)
	users := []string{"user-a", "user-b"}
	results := make([]*BTCAddress, len(users))
	var wg sync.WaitGroup
	for i, user := range users {
		wg.Add(1)
		go func(i int, user string) {
			defer wg.Done()
			addr, err := svc.GetOrCreateAddress(context.Background(), user)
			if err != nil {
				t.Errorf("GetOrCreateAddress(%s): %v", user, err)
				return
			}
			results[i] = addr
		}(i, user)
	}
	wg.Wait()
	if results[0] == nil || results[1] == nil {
		t.Fatal("missing address result")
	}
	if results[0].Address == results[1].Address {
		t.Fatalf("two users received same BTC address: %s", results[0].Address)
	}
	if results[0].DerivationIndex == results[1].DerivationIndex {
		t.Fatalf("two users received same derivation index: %d", results[0].DerivationIndex)
	}
	for i, user := range users {
		active := store.activeAddressesFor(user, string(Regtest))
		if len(active) != 1 || active[0].UserID != user || active[0].Address != results[i].Address {
			t.Fatalf("bad ownership for %s: active=%+v result=%+v", user, active, results[i])
		}
	}
}

func TestGetOrCreateAddressSameUserDifferentNetworksIndependent(t *testing.T) {
	cfgRegtest := provisioningTestConfig(t, Regtest)
	store := newFakeBTCStore(cfgRegtest)
	regtestSvc := &Service{cfg: cfgRegtest, provider: &fakeBTCProvider{}, repo: store}
	cfgTestnet := provisioningTestConfig(t, Testnet)
	testnetSvc := &Service{cfg: cfgTestnet, provider: &fakeBTCProvider{}, repo: store}

	regtestAddr, err := regtestSvc.GetOrCreateAddress(context.Background(), "user-network")
	if err != nil {
		t.Fatalf("regtest GetOrCreateAddress: %v", err)
	}
	testnetAddr, err := testnetSvc.GetOrCreateAddress(context.Background(), "user-network")
	if err != nil {
		t.Fatalf("testnet GetOrCreateAddress: %v", err)
	}
	if regtestAddr.Network != string(Regtest) || testnetAddr.Network != string(Testnet) {
		t.Fatalf("network mismatch: regtest=%+v testnet=%+v", regtestAddr, testnetAddr)
	}
	if regtestAddr.Address == testnetAddr.Address {
		t.Fatalf("different networks returned same address: %s", regtestAddr.Address)
	}
	if len(store.activeAddressesFor("user-network", string(Regtest))) != 1 {
		t.Fatal("missing regtest active address")
	}
	if len(store.activeAddressesFor("user-network", string(Testnet))) != 1 {
		t.Fatal("missing testnet active address")
	}
}

func TestGetOrCreateAddressInsertConflictReturnsPersistedActive(t *testing.T) {
	svc, store, _ := newHardeningTestService(t)
	store.conflictAfterDerive = true
	addr, err := svc.GetOrCreateAddress(context.Background(), "user-conflict")
	if err != nil {
		t.Fatalf("GetOrCreateAddress: %v", err)
	}
	if len(store.derivedNotPersisted) != 1 {
		t.Fatalf("want one non-persisted derived address, got %d", len(store.derivedNotPersisted))
	}
	if addr.Address == store.derivedNotPersisted[0].Address {
		t.Fatalf("returned phantom derived address %s", addr.Address)
	}
	active := store.activeAddressesFor("user-conflict", string(Regtest))
	if len(active) != 1 || active[0].Address != addr.Address {
		t.Fatalf("want returned persisted active address, active=%+v returned=%+v", active, addr)
	}
}

func TestGetOrCreateAddressRetryAfterInsertFailureDoesNotReturnPhantom(t *testing.T) {
	svc, store, _ := newHardeningTestService(t)
	store.failAfterDerive = true
	first, err := svc.GetOrCreateAddress(context.Background(), "user-retry")
	if err == nil || first != nil {
		t.Fatalf("first call should fail without returning an address, got addr=%+v err=%v", first, err)
	}
	if len(store.activeAddressesFor("user-retry", string(Regtest))) != 0 {
		t.Fatal("failed insert left active address behind")
	}
	retry, err := svc.GetOrCreateAddress(context.Background(), "user-retry")
	if err != nil {
		t.Fatalf("retry GetOrCreateAddress: %v", err)
	}
	if len(store.derivedNotPersisted) != 1 {
		t.Fatalf("want one non-persisted derived address, got %d", len(store.derivedNotPersisted))
	}
	if retry.Address == store.derivedNotPersisted[0].Address {
		t.Fatalf("retry returned non-persisted phantom address %s", retry.Address)
	}
	active := store.activeAddressesFor("user-retry", string(Regtest))
	if len(active) != 1 || active[0].Address != retry.Address {
		t.Fatalf("retry did not return persisted active address, active=%+v retry=%+v", active, retry)
	}
}

func TestGetOrCreateAddressRestartKeepsStableAddress(t *testing.T) {
	cfg := provisioningTestConfig(t, Regtest)
	store := newFakeBTCStore(cfg)
	svc1 := &Service{cfg: cfg, provider: &fakeBTCProvider{}, repo: store}
	first, err := svc1.GetOrCreateAddress(context.Background(), "user-restart")
	if err != nil {
		t.Fatalf("first GetOrCreateAddress: %v", err)
	}
	svc2 := &Service{cfg: cfg, provider: &fakeBTCProvider{}, repo: store}
	second, err := svc2.GetOrCreateAddress(context.Background(), "user-restart")
	if err != nil {
		t.Fatalf("second GetOrCreateAddress: %v", err)
	}
	if first.Address != second.Address || first.DerivationIndex != second.DerivationIndex {
		t.Fatalf("restart changed address: first=%+v second=%+v", first, second)
	}
}

func provisioningTestConfig(t *testing.T, network Network) *Config {
	t.Helper()
	seed := sha256.Sum256([]byte("chainfx-btc-provisioning-" + string(network)))
	master, err := NewMasterKeyForNetwork(seed[:], network)
	if err != nil {
		t.Fatal(err)
	}
	xpriv, err := DeriveAccountXPriv(master, network)
	if err != nil {
		t.Fatal(err)
	}
	return &Config{
		Enabled:             true,
		Network:             network,
		XPub:                xpriv.Neuter().String(),
		MinConfirmations:    3,
		DepositScanInterval: time.Second,
		TxScanInterval:      time.Second,
	}
}
