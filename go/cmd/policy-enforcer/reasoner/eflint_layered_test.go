package reasoner

import (
	"reflect"
	"strings"
	"testing"
)

func TestQuoteEflintLiteral(t *testing.T) {
	cases := map[string]string{
		`Niels`:                                 `"Niels"`,
		`jorrit.stutterheim@cloudnation.nl`:     `"jorrit.stutterheim@cloudnation.nl"`,
		`a"b`:                                   `"a\"b"`,
		`a\b`:                                   `"a\\b"`,
	}
	for in, want := range cases {
		if got := quoteEflintLiteral(in); got != want {
			t.Errorf("quoteEflintLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildLayer3Phrases(t *testing.T) {
	got := buildLayer3Phrases(`"Niels"`, []string{"VU", "UVA", "VU"})
	want := strings.Join([]string{
		`+requester("Niels").`,
		`+requested-steward("VU").`,
		`+requested-steward("UVA").`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("Layer-3 phrases mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestIntersectPreservingOrder(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want []string
	}{
		{name: "basic", a: []string{"x", "y", "z"}, b: []string{"y", "z"}, want: []string{"y", "z"}},
		{name: "preserve a order", a: []string{"computeToData", "dataThroughTtp"}, b: []string{"dataThroughTtp", "computeToData"}, want: []string{"computeToData", "dataThroughTtp"}},
		{name: "no overlap", a: []string{"x"}, b: []string{"y"}, want: nil},
		{name: "empty a", a: nil, b: []string{"y"}, want: nil},
		{name: "empty b", a: []string{"x"}, b: nil, want: nil},
		{name: "duplicates dedup", a: []string{"x", "x", "y"}, b: []string{"x", "y"}, want: []string{"x", "y"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := intersectPreservingOrder(c.a, c.b)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("intersectPreservingOrder(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestFilterTernary(t *testing.T) {
	facts := []eflintFact{
		{
			FactType: "relation-allows-archetype",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "Niels"},
				{FactType: "data-steward", Value: "VU"},
				{FactType: "archetype", Value: "computeToData"},
			},
		},
		{
			FactType: "relation-allows-archetype",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "Niels"},
				{FactType: "data-steward", Value: "VU"},
				{FactType: "archetype", Value: "dataThroughTtp"},
			},
		},
		{
			// Different requester — must be filtered out.
			FactType: "relation-allows-archetype",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "Bob"},
				{FactType: "data-steward", Value: "VU"},
				{FactType: "archetype", Value: "computeToData"},
			},
		},
		{
			// Different fact-type — must be filtered out.
			FactType: "relation-allows-compute-provider",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "Niels"},
				{FactType: "data-steward", Value: "VU"},
				{FactType: "compute-provider", Value: "SURF"},
			},
		},
	}

	got := filterTernary(facts, "relation-allows-archetype",
		"requester", "Niels",
		"data-steward", "VU",
		"archetype")

	want := []string{"computeToData", "dataThroughTtp"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterTernary mismatch: got %v, want %v", got, want)
	}
}

func TestRequestersWithRelationTo(t *testing.T) {
	facts := []eflintFact{
		{
			FactType: "has-relation",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "alice@x"},
				{FactType: "data-steward", Value: "VU"},
			},
		},
		{
			FactType: "has-relation",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "bob@x"},
				{FactType: "data-steward", Value: "VU"},
			},
		},
		{
			// Different steward — must be filtered out.
			FactType: "has-relation",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "carol@x"},
				{FactType: "data-steward", Value: "UVA"},
			},
		},
		{
			// Duplicate of alice — must be deduplicated while preserving order.
			FactType: "has-relation",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "requester", Value: "alice@x"},
				{FactType: "data-steward", Value: "VU"},
			},
		},
	}

	got := requestersWithRelationTo(facts, "VU")
	want := []string{"alice@x", "bob@x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requestersWithRelationTo mismatch: got %v, want %v", got, want)
	}
}

func TestFilterBinary(t *testing.T) {
	facts := []eflintFact{
		{
			FactType: "steward-supports-archetype",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "data-steward", Value: "VU"},
				{FactType: "archetype", Value: "computeToData"},
			},
		},
		{
			FactType: "steward-supports-archetype",
			Arguments: []struct {
				FactType string `json:"fact-type"`
				Value    string `json:"value"`
			}{
				{FactType: "data-steward", Value: "UVA"},
				{FactType: "archetype", Value: "reproducableScience"},
			},
		},
	}
	got := filterBinary(facts, "steward-supports-archetype", "data-steward", "VU", "archetype")
	if !reflect.DeepEqual(got, []string{"computeToData"}) {
		t.Errorf("filterBinary mismatch: got %v, want [computeToData]", got)
	}
}
