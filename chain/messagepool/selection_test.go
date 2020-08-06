package messagepool

import (
	"context"
	"testing"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/messagepool/gasguess"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/types/mock"
	"github.com/filecoin-project/lotus/chain/wallet"
	"github.com/filecoin-project/specs-actors/actors/builtin"
	"github.com/filecoin-project/specs-actors/actors/crypto"
	"github.com/ipfs/go-datastore"

	_ "github.com/filecoin-project/lotus/lib/sigs/bls"
	_ "github.com/filecoin-project/lotus/lib/sigs/secp"
)

func makeTestMessage(w *wallet.Wallet, from, to address.Address, nonce uint64, gasLimit int64, gasPrice uint64) *types.SignedMessage {
	msg := &types.Message{
		From:     from,
		To:       to,
		Method:   2,
		Value:    types.FromFil(0),
		Nonce:    nonce,
		GasLimit: gasLimit,
		GasPrice: types.NewInt(gasPrice),
	}
	sig, err := w.Sign(context.TODO(), from, msg.Cid().Bytes())
	if err != nil {
		panic(err)
	}
	return &types.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}
}

func makeTestMpool() (*MessagePool, *testMpoolAPI) {
	tma := newTestMpoolAPI()
	ds := datastore.NewMapDatastore()
	mp, err := New(tma, ds, "test")
	if err != nil {
		panic(err)
	}

	return mp, tma
}

func TestMessageChains(t *testing.T) {
	mp, tma := makeTestMpool()

	// the actors
	w1, err := wallet.NewWallet(wallet.NewMemKeyStore())
	if err != nil {
		t.Fatal(err)
	}

	a1, err := w1.GenerateKey(crypto.SigTypeBLS)
	if err != nil {
		t.Fatal(err)
	}

	w2, err := wallet.NewWallet(wallet.NewMemKeyStore())
	if err != nil {
		t.Fatal(err)
	}

	a2, err := w2.GenerateKey(crypto.SigTypeBLS)
	if err != nil {
		t.Fatal(err)
	}

	block := mock.MkBlock(nil, 1, 1)
	ts := mock.TipSet(block)

	gasLimit := gasguess.Costs[gasguess.CostKey{builtin.StorageMarketActorCodeID, 2}]

	tma.setBalance(a1, 1) // in FIL

	// test chain aggregations

	// test1: 10 messages from a1 to a2, with increasing gasPerf; it should
	//        make a single chain with 10 messages given enough balance
	mset := make(map[uint64]*types.SignedMessage)
	for i := 0; i < 10; i++ {
		m := makeTestMessage(w1, a1, a2, uint64(i), gasLimit, uint64(i+1))
		mset[uint64(i)] = m
	}

	chains := mp.createMessageChains(a1, mset, ts)
	if len(chains) != 1 {
		t.Fatal("expected a single chain")
	}
	if len(chains[0].msgs) != 10 {
		t.Fatalf("expected 10 messages in the chain but got %d", len(chains[0].msgs))
	}
	for i, m := range chains[0].msgs {
		if m.Message.Nonce != uint64(i) {
			t.Fatalf("expected nonce %d but got %d", i, m.Message.Nonce)
		}
	}

	// test2 : 10 messages from a1 to a2, with decreasing gasPerf; it should
	//         make 10 chains with 1 message each
	mset = make(map[uint64]*types.SignedMessage)
	for i := 0; i < 10; i++ {
		m := makeTestMessage(w1, a1, a2, uint64(i), gasLimit, uint64(10-i))
		mset[uint64(i)] = m
	}

	chains = mp.createMessageChains(a1, mset, ts)
	if len(chains) != 10 {
		t.Fatal("expected 10 chains")
	}
	for i, chain := range chains {
		if len(chain.msgs) != 1 {
			t.Fatalf("expected 1 message in chain %d but got %d", i, len(chain.msgs))
		}
	}
	for i, chain := range chains {
		m := chain.msgs[0]
		if m.Message.Nonce != uint64(i) {
			t.Fatalf("expected nonce %d but got %d", i, m.Message.Nonce)
		}
	}

	// test3: 10 messages from a1 to a2, with gasPerf increasing in groups of 3; it should
	//        make 4 chains, the first 3 with 3 messages and the last with a single message
	mset = make(map[uint64]*types.SignedMessage)
	for i := 0; i < 10; i++ {
		m := makeTestMessage(w1, a1, a2, uint64(i), gasLimit, uint64(1+i%3))
		mset[uint64(i)] = m
	}

	chains = mp.createMessageChains(a1, mset, ts)
	if len(chains) != 4 {
		t.Fatal("expected 4 chains")
	}
	for i, chain := range chains {
		expectedLen := 3
		if i > 2 {
			expectedLen = 1
		}
		if len(chain.msgs) != expectedLen {
			t.Fatalf("expected %d message in chain %d but got %d", expectedLen, i, len(chain.msgs))
		}
	}
	nextNonce := 0
	for _, chain := range chains {
		for _, m := range chain.msgs {
			if m.Message.Nonce != uint64(nextNonce) {
				t.Fatalf("expected nonce %d but got %d", nextNonce, m.Message.Nonce)
			}
			nextNonce++
		}
	}

	// test chain breaks

	// test4: 10 messages with non-consecutive nonces; it should make a single chain with just
	//        the first message
	mset = make(map[uint64]*types.SignedMessage)
	for i := 0; i < 10; i++ {
		m := makeTestMessage(w1, a1, a2, uint64(i*2), gasLimit, uint64(i+1))
		mset[uint64(i)] = m
	}

	chains = mp.createMessageChains(a1, mset, ts)
	if len(chains) != 1 {
		t.Fatal("expected a single chain")
	}
	if len(chains[0].msgs) != 1 {
		t.Fatalf("expected 1 message in the chain but got %d", len(chains[0].msgs))
	}
	for i, m := range chains[0].msgs {
		if m.Message.Nonce != uint64(i) {
			t.Fatalf("expected nonce %d but got %d", i, m.Message.Nonce)
		}
	}

	// test5: 10 messages with increasing gasLimit, except for the 6th message which has less than
	//        the epoch gasLimit; it should create a single chain with the first 5 messages
	mset = make(map[uint64]*types.SignedMessage)
	for i := 0; i < 10; i++ {
		var m *types.SignedMessage
		if i != 5 {
			m = makeTestMessage(w1, a1, a2, uint64(i), gasLimit, uint64(i+1))
		} else {
			m = makeTestMessage(w1, a1, a2, uint64(i), 1, uint64(i+1))
		}
		mset[uint64(i)] = m
	}

	chains = mp.createMessageChains(a1, mset, ts)
	if len(chains) != 1 {
		t.Fatal("expected a single chain")
	}
	if len(chains[0].msgs) != 5 {
		t.Fatalf("expected 5 message in the chain but got %d", len(chains[0].msgs))
	}
	for i, m := range chains[0].msgs {
		if m.Message.Nonce != uint64(i) {
			t.Fatalf("expected nonce %d but got %d", i, m.Message.Nonce)
		}
	}

	// test6: one more message than what can fit in a block according to gas limit, with increasing
	//        gasPerf; it should create a single chain with the max messages
	maxMessages := int(build.BlockGasLimit / gasLimit)
	nMessages := maxMessages + 1

	mset = make(map[uint64]*types.SignedMessage)
	for i := 0; i < nMessages; i++ {
		mset[uint64(i)] = makeTestMessage(w1, a1, a2, uint64(i), gasLimit, uint64(i+1))
	}

	chains = mp.createMessageChains(a1, mset, ts)
	if len(chains) != 1 {
		t.Fatal("expected a single chain")
	}
	if len(chains[0].msgs) != maxMessages {
		t.Fatalf("expected %d message in the chain but got %d", maxMessages, len(chains[0].msgs))
	}
	for i, m := range chains[0].msgs {
		if m.Message.Nonce != uint64(i) {
			t.Fatalf("expected nonce %d but got %d", i, m.Message.Nonce)
		}
	}

	// test5: insufficient balance for all messages
	tma.setBalanceRaw(a1, types.NewInt(uint64(3*gasLimit+1)))

	mset = make(map[uint64]*types.SignedMessage)
	for i := 0; i < 10; i++ {
		mset[uint64(i)] = makeTestMessage(w1, a1, a2, uint64(i), gasLimit, uint64(i+1))
	}

	chains = mp.createMessageChains(a1, mset, ts)
	if len(chains) != 1 {
		t.Fatal("expected a single chain")
	}
	if len(chains[0].msgs) != 2 {
		t.Fatalf("expected %d message in the chain but got %d", 2, len(chains[0].msgs))
	}
	for i, m := range chains[0].msgs {
		if m.Message.Nonce != uint64(i) {
			t.Fatalf("expected nonce %d but got %d", i, m.Message.Nonce)
		}
	}

}
