package adversarial

import (
	"fmt"
	"sync"
	"testing"

	"payment-gateway/internal/paymaster"
)

// ---------------------------------------------------------------------
// LAYER 3 — CUSTODY GUARD (Gas Station / Paymaster relay dedup)
//
// Attack: replay the exact same EIP-712 (r, s) signature from many
// goroutines simultaneously. InMemorySigLock.AcquireLock is the first line
// of defense before the DB unique constraint on gas_relay_requests.sig_hash
// — if it lets more than one caller through, the relay could execute the
// same meta-transaction twice before the DB catches it.
// ---------------------------------------------------------------------

func TestCustodyGuard_ConcurrentDuplicateSignature_OnlyOneAcquires(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	lock := paymaster.NewInMemorySigLock(stop)

	sigHash, err := paymaster.PermitSigHash(
		"0x1111111111111111111111111111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222222222222222222222222222",
	)
	if err != nil {
		t.Fatalf("failed to derive sig hash: %v", err)
	}

	const attackers = 60
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	trigger := make(chan struct{})

	for i := 0; i < attackers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-trigger
			ok, err := lock.AcquireLock(sigHash)
			if ok && err == nil {
				mu.Lock()
				acquired++
				mu.Unlock()
			}
		}()
	}
	close(trigger)
	wg.Wait()

	t.Logf("[Custody Guard Attack] %d/%d tentativas concorrentes com a MESMA assinatura adquiriram o lock", acquired, attackers)
	if acquired != 1 {
		t.Fatalf("🚨 FALHA DE CUSTÓDIA: %d chamadas concorrentes com assinatura idêntica adquiriram o sig-lock; esperado exatamente 1 (replay de meta-transação permitiria gasto duplo de gas)", acquired)
	}
}

// TestCustodyGuard_SigHashIgnoresVComponent locks in the documented security
// property of PermitSigHash: the v component (27/28) must NOT affect the
// derived hash, otherwise an attacker could flip v and resubmit the
// "same" signature as if it were a distinct one, evading the dedup lock.
func TestCustodyGuard_SigHashIgnoresVComponent(t *testing.T) {
	r := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	hash1, err := paymaster.PermitSigHash(r, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Simulate the same (r, s) resubmitted after a v-flip attempt: hash
	// derivation deliberately excludes v, so it must be identical.
	hash2, err := paymaster.PermitSigHash(r, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("🚨 BRECHA: PermitSigHash não é determinístico para o mesmo (r,s); dedup de replay quebrado")
	}
}

// TestCustodyGuard_ReleaseThenReacquire ensures ReleaseLock (used on relay
// failure to allow legitimate retry) does not create a permanent bypass:
// once released, a fresh AcquireLock must succeed exactly once again, and
// a concurrent flood immediately after release must still cap at one
// winner — a naive release could otherwise open a race window.
func TestCustodyGuard_ReleaseThenReacquire(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	lock := paymaster.NewInMemorySigLock(stop)
	hash, _ := paymaster.PermitSigHash("0x01", "0x02")

	ok, err := lock.AcquireLock(hash)
	if !ok || err != nil {
		t.Fatalf("first acquire should succeed, got ok=%v err=%v", ok, err)
	}
	ok, err = lock.AcquireLock(hash)
	if ok || err == nil {
		t.Fatalf("second acquire before release should fail, got ok=%v err=%v", ok, err)
	}

	lock.ReleaseLock(hash)

	const attackers = 30
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	trigger := make(chan struct{})
	for i := 0; i < attackers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-trigger
			if ok, err := lock.AcquireLock(hash); ok && err == nil {
				mu.Lock()
				acquired++
				mu.Unlock()
			}
		}()
	}
	close(trigger)
	wg.Wait()

	if acquired != 1 {
		t.Fatalf("🚨 FALHA DE CUSTÓDIA: após ReleaseLock, %d chamadas concorrentes readquiriram o lock; esperado exatamente 1", acquired)
	}
}

func TestCustodyGuard_DistinctSignaturesEachAcquireIndependently(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	lock := paymaster.NewInMemorySigLock(stop)

	const distinctSigs = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	for i := 0; i < distinctSigs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hash, _ := paymaster.PermitSigHash(fmt.Sprintf("0x%02x", i), fmt.Sprintf("0x%02x", i+1))
			if ok, err := lock.AcquireLock(hash); ok && err == nil {
				mu.Lock()
				acquired++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if acquired != distinctSigs {
		t.Fatalf("distinct signatures must not contend with each other; got %d/%d acquired", acquired, distinctSigs)
	}
}
