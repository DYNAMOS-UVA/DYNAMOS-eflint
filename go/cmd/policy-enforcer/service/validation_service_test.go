package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// --- Test doubles -----------------------------------------------------------

type fakeProviderConfigRepo struct {
	configs map[string]*api.ProviderValidationConfig
}

func (f *fakeProviderConfigRepo) GetProviderConfig(provider string) (*api.ProviderValidationConfig, bool, error) {
	if c, ok := f.configs[provider]; ok {
		return c, true, nil
	}
	return nil, false, nil
}

type fakeRulesRepo struct {
	rules string
	err   error
}

func (f *fakeRulesRepo) GetSharedAgreementRules() (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.rules, f.rules != "", nil
}

func (f *fakeRulesRepo) SaveSharedAgreementRules(text string) error { return nil }

type fakePhraseProvider struct {
	name    string
	phrases map[string]string
}

func (p *fakePhraseProvider) Name() string { return p.name }
func (p *fakePhraseProvider) GetLayer2Phrases(steward string) (string, bool, error) {
	if v, ok := p.phrases[steward]; ok {
		return v, true, nil
	}
	return "", false, nil
}
func (p *fakePhraseProvider) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	return nil
}

// fakeReasoner records the params it receives and returns a pre-canned result.
type fakeReasoner struct {
	receivedParams reasoner.RequestApprovalParams
	called         bool
	result         *reasoner.RequestApprovalResult
	err            error

	introspectParams reasoner.IntrospectStewardClausesParams
	introspectCalled bool
	introspectResult *reasoner.StewardClauses
	introspectErr    error
}

func (r *fakeReasoner) EvaluateRequestApproval(ctx context.Context, params reasoner.RequestApprovalParams) (*reasoner.RequestApprovalResult, error) {
	r.called = true
	r.receivedParams = params
	return r.result, r.err
}
func (r *fakeReasoner) IntrospectStewardClauses(ctx context.Context, params reasoner.IntrospectStewardClausesParams) (*reasoner.StewardClauses, error) {
	r.introspectCalled = true
	r.introspectParams = params
	return r.introspectResult, r.introspectErr
}
func (r *fakeReasoner) IsRunning() bool { return true }
func (r *fakeReasoner) ValidateAndPersistModel(ctx context.Context, organization string, modelText string) error {
	return nil
}
func (r *fakeReasoner) Name() string { return "fake" }

type fakeAuthGen struct{}

func (fakeAuthGen) GenerateToken() *pb.Auth {
	return &pb.Auth{AccessToken: "test", RefreshToken: "test"}
}

// --- Tests ------------------------------------------------------------------

func newSvcWithFakes(t *testing.T) (*ValidationService, *fakeReasoner, *fakePhraseProvider, *fakePhraseProvider) {
	t.Helper()
	logger, _ := zap.NewDevelopment()

	configs := map[string]*api.ProviderValidationConfig{
		"VU":  {Name: "VU", ValidationStrategy: api.ValidationStrategyEflint},
		"UVA": {Name: "UVA", ValidationStrategy: api.ValidationStrategyEflint},
		"RUG": {Name: "RUG", ValidationStrategy: api.ValidationStrategyLegacy},
	}
	eflintProvider := &fakePhraseProvider{
		name: "eflint",
		phrases: map[string]string{
			"VU":  `+agreement("VU").`,
			"UVA": `+agreement("UVA").`,
		},
	}
	legacyProvider := &fakePhraseProvider{
		name:    "legacy",
		phrases: map[string]string{}, // RUG has no agreement
	}
	r := &fakeReasoner{}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: &fakeProviderConfigRepo{configs: configs},
		RulesRepo:          &fakeRulesRepo{rules: `// shared rules`},
		LegacyProvider:     legacyProvider,
		EflintProvider:     eflintProvider,
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})
	return svc, r, eflintProvider, legacyProvider
}

func TestValidationService_AppliesEvaluationToWireFormat(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)

	r.result = &reasoner.RequestApprovalResult{
		PermittedRequest: true,
		PerSteward: map[string]reasoner.StewardDecision{
			"VU": {
				Permitted:        true,
				Archetypes:       []string{"computeToData"},
				ComputeProviders: []string{"SURF"},
			},
			"UVA": {
				Permitted:        true,
				Archetypes:       []string{"computeToData", "dataThroughTtp"},
				ComputeProviders: []string{"SURF"},
			},
		},
	}

	resp := svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{Id: "1", UserName: "jorrit.stutterheim@cloudnation.nl"},
		DataProviders: []string{"VU", "UVA", "RUG"},
	})

	if !r.called {
		t.Fatalf("expected reasoner.EvaluateRequestApproval to be called")
	}

	// Stewards passed to the reasoner should match the request input.
	if !reflect.DeepEqual(r.receivedParams.Stewards, []string{"VU", "UVA", "RUG"}) {
		t.Errorf("unexpected stewards passed to reasoner: %v", r.receivedParams.Stewards)
	}
	if r.receivedParams.Requester != "jorrit.stutterheim@cloudnation.nl" {
		t.Errorf("unexpected requester: %q", r.receivedParams.Requester)
	}
	if _, ok := r.receivedParams.StewardPhrases["RUG"]; ok {
		t.Errorf("RUG has no agreement; phrases must not be passed for it")
	}

	// Wire-format expectations.
	if !resp.RequestApproved {
		t.Fatalf("expected RequestApproved=true")
	}
	if got := resp.ValidDataproviders["VU"]; got == nil || !reflect.DeepEqual(got.Archetypes, []string{"computeToData"}) {
		t.Errorf("unexpected VU archetypes: %+v", got)
	}
	if got := resp.ValidDataproviders["UVA"]; got == nil || !reflect.DeepEqual(got.Archetypes, []string{"computeToData", "dataThroughTtp"}) {
		t.Errorf("unexpected UVA archetypes: %+v", got)
	}
	if !containsString(resp.InvalidDataproviders, "RUG") {
		t.Errorf("expected RUG in InvalidDataproviders, got %v", resp.InvalidDataproviders)
	}
	if resp.Auth == nil {
		t.Errorf("expected auth token to be generated for approved request")
	}
	if resp.ValidArchetypes.UserName != "jorrit.stutterheim@cloudnation.nl" {
		t.Errorf("expected ValidArchetypes.UserName to be set")
	}
}

func TestValidationService_ReasonerErrorMarksAllInvalid(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)
	r.err = errors.New("eflint pool exhausted")

	resp := svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{Id: "1", UserName: "user"},
		DataProviders: []string{"VU", "UVA"},
	})

	if resp.RequestApproved {
		t.Fatalf("expected RequestApproved=false on reasoner error")
	}
	got := append([]string(nil), resp.InvalidDataproviders...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"UVA", "VU"}) {
		t.Errorf("expected both stewards marked invalid, got %v", got)
	}
}

func TestValidationService_NoAgreementsSkipsReasoner(t *testing.T) {
	svc, r, eflintProvider, _ := newSvcWithFakes(t)
	eflintProvider.phrases = map[string]string{} // no agreements

	resp := svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{Id: "1", UserName: "user"},
		DataProviders: []string{"VU"},
	})

	if r.called {
		t.Errorf("reasoner should not be called when no agreements are available")
	}
	if resp.RequestApproved {
		t.Errorf("expected RequestApproved=false")
	}
	if !containsString(resp.InvalidDataproviders, "VU") {
		t.Errorf("VU should be in InvalidDataproviders")
	}
}

// TestValidationService_GetAllowedClauses_ResolvesProviderAndPassesPhrases
// asserts that GetAllowedClausesForSteward picks the configured provider,
// loads the steward's Layer-2 phrases, hands them to the reasoner together
// with the shared rules, and returns whatever the reasoner produced.
func TestValidationService_GetAllowedClauses_ResolvesProviderAndPassesPhrases(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)
	r.introspectResult = &reasoner.StewardClauses{
		Steward:                   "VU",
		SupportedArchetypes:       []string{"computeToData", "dataThroughTtp"},
		SupportedComputeProviders: []string{"SURF"},
		Relations: []reasoner.RequesterClauses{
			{Requester: "alice@x", Archetypes: []string{"computeToData"}, ComputeProviders: []string{"SURF"}},
			{Requester: "bob@x", Archetypes: []string{"dataThroughTtp"}, ComputeProviders: []string{"SURF"}},
		},
	}

	out, err := svc.GetAllowedClausesForSteward(context.Background(), "VU", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.introspectCalled {
		t.Fatalf("expected IntrospectStewardClauses to be called")
	}
	if r.introspectParams.Steward != "VU" {
		t.Errorf("expected steward=VU, got %q", r.introspectParams.Steward)
	}
	if r.introspectParams.StewardPhrases != `+agreement("VU").` {
		t.Errorf("expected steward phrases to come from eflint provider, got %q", r.introspectParams.StewardPhrases)
	}
	if r.introspectParams.SharedRules != `// shared rules` {
		t.Errorf("expected shared rules to be loaded, got %q", r.introspectParams.SharedRules)
	}
	if len(out.Relations) != 2 {
		t.Errorf("expected both relations returned, got %d", len(out.Relations))
	}
}

func TestValidationService_GetAllowedClauses_FiltersByRequester(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)
	r.introspectResult = &reasoner.StewardClauses{
		Steward: "VU",
		Relations: []reasoner.RequesterClauses{
			{Requester: "alice@x"},
			{Requester: "bob@x"},
		},
	}

	out, err := svc.GetAllowedClausesForSteward(context.Background(), "VU", "bob@x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Relations) != 1 || out.Relations[0].Requester != "bob@x" {
		t.Errorf("expected only bob@x's relation, got %+v", out.Relations)
	}
}

func TestValidationService_GetAllowedClauses_NoAgreementReturnsNil(t *testing.T) {
	svc, r, eflintProvider, _ := newSvcWithFakes(t)
	eflintProvider.phrases = map[string]string{}

	out, err := svc.GetAllowedClausesForSteward(context.Background(), "VU", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil result for steward without agreement, got %+v", out)
	}
	if r.introspectCalled {
		t.Errorf("reasoner should not be called when steward has no agreement")
	}
}

// TestValidationService_ResolvesProviderByConfig confirms that legacy vs eflint
// providers are picked according to /policyEnforcer/configs/{provider}.
func TestValidationService_ResolvesProviderByConfig(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cfgRepo := &fakeProviderConfigRepo{configs: map[string]*api.ProviderValidationConfig{
		"X": {Name: "X", ValidationStrategy: api.ValidationStrategyLegacy},
		"Y": {Name: "Y", ValidationStrategy: api.ValidationStrategyEflint},
	}}
	legacy := &fakePhraseProvider{name: "legacy", phrases: map[string]string{"X": `+agreement("X").`}}
	eflintP := &fakePhraseProvider{name: "eflint", phrases: map[string]string{"Y": `+agreement("Y").`}}
	r := &fakeReasoner{result: &reasoner.RequestApprovalResult{
		PerSteward: map[string]reasoner.StewardDecision{
			"X": {Permitted: false, Reason: "no relation"},
			"Y": {Permitted: false, Reason: "no relation"},
		},
	}}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: cfgRepo,
		RulesRepo:          &fakeRulesRepo{rules: `// rules`},
		LegacyProvider:     legacy,
		EflintProvider:     eflintP,
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})

	_ = svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{UserName: "user"},
		DataProviders: []string{"X", "Y"},
	})

	if got := r.receivedParams.StewardPhrases["X"]; got != `+agreement("X").` {
		t.Errorf("X should resolve via legacy provider, got %q", got)
	}
	if got := r.receivedParams.StewardPhrases["Y"]; got != `+agreement("Y").` {
		t.Errorf("Y should resolve via eflint provider, got %q", got)
	}
}
