package contribution

import (
	"encoding/json"
	"testing"
)

func TestStrictPlans(t *testing.T) {
	id, e := NewOperationID()
	if e != nil {
		t.Fatal(e)
	}
	p := CreatePlan{Kind: CreateKind, Input: CreateInput{Meta: OperationMeta{SchemaVersion: 1, Namespace: "test", OperationID: id, AccountBinding: "account"}, ParentID: "folder", Title: "Title", Markdown: "body"}, Artifact: Artifact{DOCX: []byte("artifact"), Digest: hash([]byte("artifact")), RendererVersion: "test"}}
	p.Digest = createDigest(p)
	b, _ := json.Marshal(p)
	if _, e = DecodeCreatePlan(b); e != nil {
		t.Fatal(e)
	}
	p.Input.Meta.CorrelationID = "other"
	if p.Digest != createDigest(p) {
		t.Fatal("correlation affects semantic identity")
	}
	p.Input.Meta.AccountBinding = "other"
	if p.Digest == createDigest(p) {
		t.Fatal("account not bound")
	}
	for _, b := range [][]byte{[]byte(`{"kind":"create","kind":"reply"}`), append(b, []byte(` {}`)...), []byte(`{"unknown":true}`), {0xff}} {
		if _, e := DecodeCreatePlan(b); e == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
}

func TestStrictJSONTextAndFieldNames(t *testing.T) {
	var v struct {
		Text string `json:"text"`
	}
	for _, b := range []string{`{"text":"\ud800"}`, `{"text":"\udc00"}`, `{"Text":"alias"}`, `{"text":"first","Text":"second"}`, `{"text":null}`} {
		if DecodeJSON([]byte(b), &v) == nil {
			t.Fatalf("accepted %s", b)
		}
	}
	for _, b := range []string{`{"text":"\ud83d\ude00"}`, `{"text":"\\ud800"}`} {
		if e := DecodeJSON([]byte(b), &v); e != nil {
			t.Fatalf("valid text %s: %v", b, e)
		}
	}
}
