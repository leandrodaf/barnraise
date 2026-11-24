package nats

import "testing"

func TestSignVerify(t *testing.T) {
	seed, pub, err := NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	gotPub, sig, err := Sign(seed, []byte("head"))
	if err != nil || gotPub != pub {
		t.Fatalf("sign: %s %v", gotPub, err)
	}
	if err := VerifySigned(pub, []byte("head"), sig); err != nil {
		t.Fatal(err)
	}
	if VerifySigned(pub, []byte("other"), sig) == nil {
		t.Fatal("changed message verified")
	}
	_, other, _ := NewMachineKey()
	if VerifySigned(other, []byte("head"), sig) == nil {
		t.Fatal("other key verified")
	}
	if VerifySigned("", nil, "") == nil || VerifySigned("bad", nil, "x") == nil || VerifySigned(pub, nil, "%%%") == nil {
		t.Fatal("malformed input verified")
	}
	if _, _, err := Sign("bad", nil); err == nil {
		t.Fatal("bad seed signed")
	}
}
