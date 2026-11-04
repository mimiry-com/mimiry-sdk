package mimiry

// The contract test: the SDK's types against the published API documents,
// spec/compute.json and spec/org.json.
//
// The types follow what api-compute actually sends and accepts (see
// compute_types.go); the document is what customers read. Where the two
// disagree, one of them is wrong, and nothing else would notice — the SDK
// works, the document misleads. So every mapped struct must carry exactly the
// properties its schema declares, every option struct must send exactly the
// query parameters its operation declares, and every state constant must be in
// the document's enum. An exception is allowed only with a reason, and an
// exception that no longer matches anything fails the test.
//
// spec/*.json are verbatim copies of the platform's published documents, kept
// beside the SDK so this test needs nothing outside the repo. The platform's
// spec validation fails when a copy drifts.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mimiry-com/mimiry-sdk/internal/apispec"
)

const specDir = "../spec"

// spec wraps the shared document reader with the test's own failure handling.
type spec struct {
	t    *testing.T
	docs map[string]*apispec.Doc
}

func loadSpec(t *testing.T) *spec {
	t.Helper()
	docs, err := apispec.LoadAll(specDir)
	if err != nil {
		t.Fatal(err)
	}
	s := &spec{t: t, docs: map[string]*apispec.Doc{}}
	for _, d := range docs {
		s.docs[d.Name] = d
	}
	return s
}

func (s *spec) doc(name string) *apispec.Doc {
	d, ok := s.docs[name]
	if !ok {
		s.t.Fatalf("no document %s in %s", name, specDir)
	}
	return d
}

// properties is the property names of the schema at ptr in a document.
func (s *spec) properties(t *testing.T, doc, ptr string) map[string]bool {
	t.Helper()
	m, err := s.doc(doc).At(ptr)
	if err != nil {
		t.Fatal(err)
	}
	props, err := s.doc(doc).Properties(m)
	if err != nil {
		t.Fatal(err)
	}
	return props
}

func (s *spec) queryParams(t *testing.T, doc, path, method string) map[string]bool {
	t.Helper()
	q, err := s.doc(doc).QueryParams(path, method)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func (s *spec) enum(t *testing.T, doc, ptr string) map[string]bool {
	t.Helper()
	m, err := s.doc(doc).At(ptr)
	if err != nil {
		t.Fatal(err)
	}
	vals, ok := m["enum"].([]any)
	if !ok {
		t.Fatalf("%s %s has no enum", doc, ptr)
	}
	out := map[string]bool{}
	for _, v := range vals {
		out[v.(string)] = true
	}
	return out
}

// jsonFields is a struct's JSON property names, embedded structs flattened.
func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && tag == "" {
			for k := range jsonFields(f.Type) {
				out[k] = true
			}
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	return out
}

// exceptions: "<what>.<name>" → why the SDK and the document may differ.
// Every entry must still match a difference, or the test fails: an exception
// that excuses nothing hides the next real one.
var exceptions = map[string]string{
	"LogsOptions.timestamps":                "deprecated in the document and has no effect; the SDK does not send it",
	"UpdateOrgRequest.require_2fa":          "deprecated in the document and not enforced; the SDK does not offer to set it",
	"UpdateOrgRequest.allow_member_invites": "deprecated in the document and not in effect; the SDK does not offer to set it",
}

func diff(t *testing.T, what string, sdk, doc map[string]bool, used map[string]bool) {
	t.Helper()
	var onlySDK, onlyDoc []string
	for k := range sdk {
		if !doc[k] {
			onlySDK = append(onlySDK, k)
		}
	}
	for k := range doc {
		if !sdk[k] {
			onlyDoc = append(onlyDoc, k)
		}
	}
	sort.Strings(onlySDK)
	sort.Strings(onlyDoc)
	for _, k := range onlySDK {
		key := what + "." + k
		if _, ok := exceptions[key]; ok {
			used[key] = true
			continue
		}
		t.Errorf("%s: the SDK has %q, which the document does not declare", what, k)
	}
	for _, k := range onlyDoc {
		key := what + "." + k
		if _, ok := exceptions[key]; ok {
			used[key] = true
			continue
		}
		t.Errorf("%s: the document declares %q, which the SDK does not have", what, k)
	}
}

func TestTypesMatchTheDocument(t *testing.T) {
	s := loadSpec(t)
	used := map[string]bool{}

	schemas := []struct {
		typ reflect.Type
		doc string
		ptr string
	}{
		{reflect.TypeFor[Session](), "compute.json", "#/components/schemas/SessionResponse"},
		{reflect.TypeFor[SSHInfo](), "compute.json", "#/components/schemas/SessionResponse/properties/ssh"},
		{reflect.TypeFor[SessionBilling](), "compute.json", "#/components/schemas/SessionResponse/properties/billing"},
		{reflect.TypeFor[SessionEvent](), "compute.json", "#/components/schemas/SessionResponse/properties/events"},
		{reflect.TypeFor[AutoTerminate](), "compute.json", "#/components/schemas/SessionResponse/properties/auto_terminate"},
		{reflect.TypeFor[CreateSessionRequest](), "compute.json", "#/components/schemas/CreateSessionRequest"},
		{reflect.TypeFor[Image](), "compute.json", "#/components/schemas/CreateSessionRequest/properties/image"},
		{reflect.TypeFor[GPUSpec](), "compute.json", "#/components/schemas/CreateSessionRequest/properties/gpu"},
		{reflect.TypeFor[MemorySpec](), "compute.json", "#/components/schemas/CreateSessionRequest/properties/memory"},
		{reflect.TypeFor[VolumeMount](), "compute.json", "#/components/schemas/VolumeMountSpec"},
		{reflect.TypeFor[Accepted](), "compute.json", "#/components/schemas/AcceptedResponse"},
		{reflect.TypeFor[SessionLogs](), "compute.json", "#/components/schemas/SessionLogsResponse"},
		{reflect.TypeFor[Volume](), "compute.json", "#/components/schemas/VolumeResponse"},
		{reflect.TypeFor[CreateVolumeRequest](), "compute.json", "#/components/schemas/CreateVolumeRequest"},
		{reflect.TypeFor[CatalogEntry](), "compute.json", "#/components/schemas/GPUCatalogEntry"},
		{reflect.TypeFor[GPUModel](), "compute.json", "#/components/schemas/GPUModelAvailability"},
		{reflect.TypeFor[ProviderPrice](), "compute.json", "#/components/schemas/ProviderPrice"},
		{reflect.TypeFor[SKU](), "compute.json", "#/components/schemas/SKU"},
		{reflect.TypeFor[Listing](), "compute.json", "#/components/schemas/SKUListing"},
		{reflect.TypeFor[Configuration](), "compute.json", "#/components/schemas/GPUConfiguration"},
		{reflect.TypeFor[Balance](), "compute.json", "#/components/schemas/BalanceResponse"},
		{reflect.TypeFor[Transaction](), "compute.json", "#/components/schemas/TransactionResponse"},
		{reflect.TypeFor[Quota](), "compute.json", "#/components/schemas/QuotaInfo"},
		{reflect.TypeFor[Org](), "org.json", "#/components/schemas/Organization"},
		{reflect.TypeFor[Member](), "org.json", "#/components/schemas/Member"},
		{reflect.TypeFor[OrgSettings](), "org.json", "#/components/schemas/Settings"},
		{reflect.TypeFor[Invitation](), "org.json", "#/components/schemas/Invitation"},
		{reflect.TypeFor[InvitationPreview](), "org.json", "#/components/schemas/InvitationPreview"},
		{reflect.TypeFor[CreateOrgRequest](), "org.json", "#/components/schemas/CreateOrgRequest"},
		{reflect.TypeFor[UpdateOrgRequest](), "org.json", "#/components/schemas/UpdateOrgRequest"},
		{reflect.TypeFor[InviteRequest](), "org.json", "#/components/schemas/InviteMemberRequest"},
		{reflect.TypeFor[WindDownRequest](), "org.json", "#/components/schemas/WindDownRequest"},
		{reflect.TypeFor[OperationAccepted](), "org.json", "#/components/responses/Accepted/content/application~1json/schema"},
		{reflect.TypeFor[Operation](), "org.json", "#/paths/~1api~1organizations~1v1~1operations~1{request_id}/get/responses/200/content/application~1json/schema"},
	}
	for _, m := range schemas {
		t.Run(m.typ.Name(), func(t *testing.T) {
			diff(t, m.typ.Name(), jsonFields(m.typ), s.properties(t, m.doc, m.ptr), used)
		})
	}

	// Option structs: fill every field, and compare the keys query() actually
	// sends with the operation's declared parameters.
	queries := []struct {
		name, doc, path, method string
		send                    func() url.Values
	}{
		{"SessionListOptions", "compute.json", "/api/compute/v1/sessions", "get",
			func() url.Values { var o SessionListOptions; fill(&o); return o.query() }},
		{"GetSessionOptions", "compute.json", "/api/compute/v1/sessions/{id}", "get",
			func() url.Values { var o GetSessionOptions; fill(&o); return o.query() }},
		{"LogsOptions", "compute.json", "/api/compute/v1/sessions/{id}/logs", "get",
			func() url.Values { var o LogsOptions; fill(&o); return o.query() }},
		{"VolumeListOptions", "compute.json", "/api/compute/v1/volumes", "get",
			func() url.Values { var o VolumeListOptions; fill(&o); return o.query() }},
		{"AvailabilityOptions", "compute.json", "/api/compute/v1/availability", "get",
			func() url.Values { var o AvailabilityOptions; fill(&o); return o.query() }},
		{"TransactionListOptions", "compute.json", "/api/compute/v1/transactions", "get",
			func() url.Values { var o TransactionListOptions; fill(&o); return o.query() }},
		{"OrgListOptions", "org.json", "/api/organizations/v1", "get",
			func() url.Values { var o OrgListOptions; fill(&o); return o.query() }},
		{"ListOptions", "org.json", "/api/organizations/v1/{id}/invitations", "get",
			func() url.Values { var o ListOptions; fill(&o); q := url.Values{}; o.encode(q); return q }},
	}
	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			sent := map[string]bool{}
			for k := range q.send() {
				sent[k] = true
			}
			diff(t, q.name, sent, s.queryParams(t, q.doc, q.path, q.method), used)
		})
	}

	// State constants against the document's enums.
	enums := []struct{ goType, file, doc, ptr string }{
		{"SessionState", "compute_types.go", "compute.json", "#/components/schemas/SessionState"},
		{"VolumeState", "compute_types.go", "compute.json", "#/components/schemas/VolumeState"},
		{"OrgRole", "orgs_types.go", "org.json", "#/components/schemas/Member/properties/role"},
		{"OperationStatus", "orgs_types.go", "org.json",
			"#/paths/~1api~1organizations~1v1~1operations~1{request_id}/get/responses/200/content/application~1json/schema/properties/status"},
	}
	for _, e := range enums {
		t.Run(e.goType, func(t *testing.T) {
			diff(t, e.goType, constValues(t, e.file, e.goType), s.enum(t, e.doc, e.ptr), used)
		})
	}

	for k, why := range exceptions {
		if !used[k] {
			t.Errorf("exception %q (%s) excuses no difference any more — remove it", k, why)
		}
	}
}

// fill sets every exported field of *p to a non-zero value, so query() emits
// every parameter it knows.
func fill(p any) {
	fillValue(reflect.ValueOf(p).Elem())
}

func fillValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillValue(v.Field(i))
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillValue(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillValue(v.Index(0))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(1.5)
	}
}

// constValues reads the string constants of a type from this package's
// source, so the test needs no hand-kept copy of them.
func constValues(t *testing.T, file, typeName string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != typeName {
				continue
			}
			for _, val := range vs.Values {
				if lit, ok := val.(*ast.BasicLit); ok {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						out[s] = true
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s constants found in %s — the read is broken", typeName, file)
	}
	return out
}
