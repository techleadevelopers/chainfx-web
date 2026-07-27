package server

import "testing"

func TestValidCPF(t *testing.T) {
	tests := []struct {
		name string
		cpf  string
		want bool
	}{
		{name: "valid digits", cpf: "12345678909", want: true},
		{name: "valid formatted", cpf: "935.411.347-80", want: true},
		{name: "repeated digits", cpf: "11111111111", want: false},
		{name: "invalid verifier", cpf: "12345678901", want: false},
		{name: "short", cpf: "123", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validCPF(tt.cpf); got != tt.want {
				t.Fatalf("validCPF(%q) = %v, want %v", tt.cpf, got, tt.want)
			}
		})
	}
}

func TestValidateEfiPixCustomerRequiresValidCPF(t *testing.T) {
	if err := validateEfiPixCustomer(paymentCustomerInput{Name: "Cliente Teste", CPF: "12345678909"}); err != nil {
		t.Fatalf("expected valid customer, got %v", err)
	}
	if err := validateEfiPixCustomer(paymentCustomerInput{Name: "Cliente Teste", CPF: "12345678901"}); err == nil {
		t.Fatal("expected invalid CPF to be rejected")
	}
	if err := validateEfiPixCustomer(paymentCustomerInput{Name: "Cliente Teste"}); err == nil {
		t.Fatal("expected missing CPF to be rejected")
	}
	if err := validateEfiPixCustomer(paymentCustomerInput{CPF: "12345678909"}); err == nil {
		t.Fatal("expected missing name to be rejected")
	}
}

func TestBuildEfiDebtorSkipsInvalidCPF(t *testing.T) {
	if debtor := buildEfiDebtor(paymentCustomerInput{Name: "Cliente Teste", CPF: "12345678901"}); debtor != nil {
		t.Fatalf("expected invalid CPF to be skipped, got %#v", debtor)
	}
	debtor := buildEfiDebtor(paymentCustomerInput{Name: "Cliente Teste", CPF: "123.456.789-09"})
	if debtor["cpf"] != "12345678909" || debtor["nome"] != "Cliente Teste" {
		t.Fatalf("unexpected debtor payload: %#v", debtor)
	}
}
