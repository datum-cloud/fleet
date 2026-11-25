package v1alpha1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/model"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/cel/environment"
	"sigs.k8s.io/yaml"
)

func ptr[T any](value T) *T { return &value }

// layoutSwitch is a synthetic 48 RJ45 plus 8 QSFP28 switch: two RJ45 banks
// numbered odd-top, even-bottom, a QSFP28 bank with one double-wide cage, and
// rear PSUs placed by offset.
func layoutSwitch() *DeviceType {
	_, model := catalogRecords()
	model.Spec.ModelShape = &ModelShape{HeightUnits: ptr(1.0), FullDepth: ptr(true), Width: ChassisFullWidth}
	model.Spec.Faceplate = &Faceplate{Banks: []FaceplateBank{
		{ID: "rj45-a", Face: ChassisFront, Rows: 2, Columns: 12},
		{ID: "rj45-b", Face: ChassisFront, Rows: 2, Columns: 12},
		{ID: "qsfp", Face: ChassisFront, Rows: 2, Columns: 5},
		{ID: "psu-1", Face: ChassisRear, Rows: 1, Columns: 1, Offset: ptr(0.0), CellMM: &CellSize{Width: 40, Height: 38}},
		{ID: "psu-2", Face: ChassisRear, Rows: 1, Columns: 1, Offset: ptr(0.89), CellMM: &CellSize{Width: 40, Height: 38}},
	}}
	model.Spec.ComponentTemplates = nil
	for port := int32(1); port <= 48; port++ {
		bank, index := "rj45-a", port
		if port > 24 {
			bank, index = "rj45-b", port-24
		}
		model.Spec.ComponentTemplates = append(model.Spec.ComponentTemplates, ComponentTemplate{
			ID: fmt.Sprintf("ge%d", port), Name: fmt.Sprintf("Ethernet %d", port), Type: "NetworkPort", Connector: "RJ45", Face: ChassisFront,
			Layout: &TemplateLayout{Bank: bank, Row: 2 - index%2, Column: (index + 1) / 2},
		})
	}
	for port := int32(1); port <= 8; port++ {
		layout := &TemplateLayout{Bank: "qsfp", Row: 2 - port%2, Column: (port + 1) / 2}
		model.Spec.ComponentTemplates = append(model.Spec.ComponentTemplates, ComponentTemplate{ID: fmt.Sprintf("q%d", port), Name: fmt.Sprintf("QSFP %d", port), Type: "NetworkPort", Connector: "QSFP28", Face: ChassisFront, Layout: layout})
	}
	// A double-wide cage in the last column pair of the top row.
	model.Spec.ComponentTemplates = append(model.Spec.ComponentTemplates,
		ComponentTemplate{ID: "dd1", Name: "QSFP-DD 1", Type: "NetworkPort", Connector: "QSFP-DD", Face: ChassisFront, Layout: &TemplateLayout{Bank: "qsfp", Row: 2, Column: 5}},
		ComponentTemplate{ID: "psu1", Name: "PSU 1", Type: "PowerInlet", Connector: "IEC C14", Face: ChassisRear, Layout: &TemplateLayout{Bank: "psu-1", Row: 1, Column: 1}},
		ComponentTemplate{ID: "psu2", Name: "PSU 2", Type: "PowerInlet", Connector: "IEC C14", Face: ChassisRear, Layout: &TemplateLayout{Bank: "psu-2", Row: 1, Column: 1}},
		// Recorded but not laid out: the readers put it in a tray.
		ComponentTemplate{ID: "console", Name: "Console", Type: "ConsolePort", Connector: "RJ45", Face: ChassisRear},
	)
	return model
}

func TestFaceplateLayoutIsAccepted(t *testing.T) {
	model := layoutSwitch()
	if errs := admit(t, "DeviceType", object(t, model), nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	// Span: one module across two columns beside a single-width one.
	model.Spec.Faceplate.Banks[2].Columns = 6
	model.Spec.ComponentTemplates[56].Layout = &TemplateLayout{Bank: "qsfp", Row: 2, Column: 5, Span: 2}
	if errs := admit(t, "DeviceType", object(t, model), nil); len(errs) > 0 {
		t.Fatalf("span inside the grid rejected: %v", errs)
	}
	// Offsets are per face: the fixture flows its front and places its
	// rear. Placing every front bank too is also valid.
	for i, offset := range []float64{0, 0.36, 0.72} {
		model.Spec.Faceplate.Banks[i].Offset = ptr(offset)
	}
	if errs := admit(t, "DeviceType", object(t, model), nil); len(errs) > 0 {
		t.Fatalf("placed front and rear rejected: %v", errs)
	}
	// A layout is optional on every template and on the model.
	_, plain := catalogRecords()
	if errs := admit(t, "DeviceType", object(t, plain), nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	stored := object(t, model)
	if errs := admit(t, "DeviceType", stored, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, template := range stored["spec"].(map[string]any)["componentTemplates"].([]any) {
		if layout, ok := template.(map[string]any)["layout"].(map[string]any); ok {
			if _, defaulted := layout["span"]; defaulted && template.(map[string]any)["id"] != "dd1" {
				t.Fatalf("span was defaulted on %v", template)
			}
		}
	}
}

func TestFaceplateLayoutRules(t *testing.T) {
	for name, test := range map[string]struct {
		mutate  func(*DeviceType)
		message string
	}{
		"layout without a faceplate": {func(m *DeviceType) { m.Spec.Faceplate = nil }, "own face"},
		"layout without a face":      {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Face = "" }, "own face"},
		"unknown bank":               {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Bank = "missing" }, "own face"},
		"bank on the other face":     {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Face = ChassisRear }, "own face"},
		"row below the grid":         {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Row = 3 }, "inside its bank"},
		"column past the grid":       {func(m *DeviceType) { m.Spec.ComponentTemplates[0].Layout.Column = 13 }, "inside its bank"},
		"span past the grid":         {func(m *DeviceType) { m.Spec.ComponentTemplates[56].Layout.Span = 2 }, "inside its bank"},
		"two templates in one cell":  {func(m *DeviceType) { m.Spec.ComponentTemplates[1].Layout = m.Spec.ComponentTemplates[0].Layout }, "overlap"},
		"span over a neighbour": {func(m *DeviceType) {
			m.Spec.ComponentTemplates[0].Layout = &TemplateLayout{Bank: "rj45-a", Row: 1, Column: 1, Span: 2}
		}, "overlap"},
		"offset on some banks of a face":  {func(m *DeviceType) { m.Spec.Faceplate.Banks[0].Offset = ptr(0.0) }, "offsets"},
		"offset missing on one rear bank": {func(m *DeviceType) { m.Spec.Faceplate.Banks[4].Offset = nil }, "offsets"},
		"offset past the face":            {func(m *DeviceType) { m.Spec.Faceplate.Banks[3].Offset = ptr(1.5) }, ""},
		"duplicate bank id":               {func(m *DeviceType) { m.Spec.Faceplate.Banks[1].ID = "rj45-a" }, ""},
		"bank without a face":             {func(m *DeviceType) { m.Spec.Faceplate.Banks[0].Face = "" }, ""},
		"too many rows":                   {func(m *DeviceType) { m.Spec.Faceplate.Banks[0].Rows = 9 }, ""},
		"zero columns":                    {func(m *DeviceType) { m.Spec.Faceplate.Banks[0].Columns = 0 }, ""},
		"negative span":                   {func(m *DeviceType) { m.Spec.ComponentTemplates[56].Layout.Span = -1 }, ""},
		"empty faceplate":                 {func(m *DeviceType) { m.Spec.Faceplate.Banks = nil }, ""},
		"unknown width":                   {func(m *DeviceType) { m.Spec.ModelShape.Width = "Quarter" }, ""},
		"zero-size module cell":           {func(m *DeviceType) { m.Spec.Faceplate.Banks[3].CellMM.Width = 0 }, ""},
	} {
		t.Run(name, func(t *testing.T) {
			model := layoutSwitch()
			test.mutate(model)
			errs := admit(t, "DeviceType", object(t, model), nil)
			if len(errs) == 0 {
				t.Fatal("accepted")
			}
			if test.message != "" && !strings.Contains(errs.ToAggregate().Error(), test.message) {
				t.Fatalf("rejected for another reason: %v", errs)
			}
		})
	}
}

// Width is shape, not layout: it sits on ModelShape on both kinds, as depth
// does, and is never defaulted.
func TestModelShapeWidthIsOptionalOnBothKinds(t *testing.T) {
	_, model := catalogRecords()
	for _, width := range []ChassisWidth{"", ChassisFullWidth, ChassisHalfWidth} {
		model.Spec.ModelShape = &ModelShape{Width: width}
		device := syntheticDevice()
		device.Spec.ModelShape = &ModelShape{Width: width}
		for kind, record := range map[string]map[string]any{"DeviceType": object(t, model), "Device": object(t, device)} {
			if errs := admit(t, kind, record, nil); len(errs) > 0 {
				t.Fatalf("%s width %q: %v", kind, width, errs)
			}
			if got, ok := record["spec"].(map[string]any)["modelShape"].(map[string]any)["width"]; width == "" && ok {
				t.Fatalf("%s width defaulted to %v", kind, got)
			}
		}
	}
}

// worstCaseLayout is the costliest valid layout found for the overlap rule:
// 32 banks of one row with maximum-length IDs, and every laid-out template in
// that one row, so every pair passes the row check and pairs with the same
// column range also reach the bank comparison. A single 4x64 bank, or full
// spans across 8 rows, cost less. The other templates carry no layout.
func worstCaseLayout(laidOut int) *DeviceType {
	_, model := catalogRecords()
	id := func(n int) string { return fmt.Sprintf("%s%02d", strings.Repeat("b", 61), n) }
	model.Spec.Faceplate = &Faceplate{}
	for n := range 32 {
		model.Spec.Faceplate.Banks = append(model.Spec.Faceplate.Banks, FaceplateBank{ID: id(n), Face: ChassisFront, Rows: 1, Columns: 64})
	}
	model.Spec.ComponentTemplates = nil
	for n := range 256 {
		template := ComponentTemplate{ID: fmt.Sprintf("%s%03d", strings.Repeat("t", 60), n), Name: fmt.Sprintf("Port %d", n), Type: "NetworkPort", Face: ChassisFront}
		if n < laidOut {
			template.Layout = &TemplateLayout{Bank: id(n % 32), Row: 1, Column: int32(n/32)*8 + 1, Span: 8}
		}
		model.Spec.ComponentTemplates = append(model.Spec.ComponentTemplates, template)
	}
	return model
}

// runtimeCost is the CEL cost the API server charges an object on create.
func runtimeCost(t *testing.T, record *DeviceType) (int64, field.ErrorList) {
	t.Helper()
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(loadCRDs(t)["DeviceType"].Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	errs, remaining := cel.NewValidator(structural, true, celconfig.PerCallLimit).Validate(context.Background(), nil, structural, object(t, record), nil, celconfig.RuntimeCELCostBudget)
	return celconfig.RuntimeCELCostBudget - remaining, errs
}

// TestFaceplateRulesFitTheCELBudget records the estimated cost of each
// layout rule against the static per-expression and per-CRD limits, and
// admits the worst-case object under the runtime per-call limit and
// per-object budget the API server uses.
func TestFaceplateRulesFitTheCELBudget(t *testing.T) {
	crd := loadCRDs(t)["DeviceType"]
	var schema apiextensions.JSONSchemaProps
	if err := extensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &schema, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&schema)
	if err != nil {
		t.Fatal(err)
	}
	spec := structural.Properties["spec"]
	envSet := environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion())
	// None of these nodes sits inside a list, so each rule runs once per
	// object and its estimated cost is also its share of the CRD total.
	var total uint64
	for _, node := range []struct {
		path   string
		schema *structuralschema.Structural
	}{
		{"spec", &spec},
		{"spec.faceplate", ptr(spec.Properties["faceplate"])},
		{"spec.componentTemplates", ptr(spec.Properties["componentTemplates"])},
	} {
		results, err := cel.Compile(node.schema, model.SchemaDeclType(node.schema, false), celconfig.PerCallLimit, envSet, cel.NewExpressionsEnvLoader())
		if err != nil {
			t.Fatal(err)
		}
		for i, result := range results {
			if result.Error != nil {
				t.Fatalf("%s rule %d: %v", node.path, i, result.Error)
			}
			total += result.MaxCost
			t.Logf("%s rule %d: estimated cost %d (per-expression limit %d)", node.path, i, result.MaxCost, crdvalidation.StaticEstimatedCostLimit)
			if result.MaxCost > crdvalidation.StaticEstimatedCostLimit {
				t.Fatalf("%s rule %d estimated cost %d exceeds %d", node.path, i, result.MaxCost, crdvalidation.StaticEstimatedCostLimit)
			}
		}
	}
	t.Logf("DeviceType layout rules total %d (CRD limit %d)", total, crdvalidation.StaticEstimatedCRDCostLimit)
	if total > crdvalidation.StaticEstimatedCRDCostLimit {
		t.Fatalf("DeviceType rules estimated %d, over the CRD limit %d", total, crdvalidation.StaticEstimatedCRDCostLimit)
	}
	// Static estimates assume all 256 templates are laid out; the runtime
	// cap of 128 is what keeps the real cost inside the per-call limit.
	worst := worstCaseLayout(128)
	cost, errs := runtimeCost(t, worst)
	t.Logf("worst-case 128 laid-out templates: runtime cost %d (per-call limit %d)", cost, celconfig.PerCallLimit)
	if len(errs) > 0 {
		t.Fatalf("worst-case layout rejected: %v", errs)
	}
	// Keep headroom: the whole object, every rule included, under 75% of
	// what one rule may spend.
	if cost > celconfig.PerCallLimit*3/4 {
		t.Fatalf("worst-case runtime cost %d leaves too little headroom under %d", cost, celconfig.PerCallLimit)
	}
	// One overlap in the worst case is reported as an overlap, not a cost error.
	worst.Spec.ComponentTemplates[127].Layout = worst.Spec.ComponentTemplates[0].Layout
	if _, errs := runtimeCost(t, worst); len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "overlap") || strings.Contains(errs.ToAggregate().Error(), "cost") {
		t.Fatalf("worst-case overlap: %v", errs)
	}
	// Past the cap the object is rejected and says why, even where the
	// overlap rule would then run out of budget.
	for _, laidOut := range []int{129, 256} {
		if errs := admit(t, "DeviceType", object(t, worstCaseLayout(laidOut)), nil); len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "at most 128 templates") {
			t.Fatalf("%d laid-out templates: %v", laidOut, errs)
		}
	}
}

// The hardware catalog doc's synthetic example must stay admissible.
func TestHardwareCatalogDocExampleIsAdmitted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "development", "hardware-catalog.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, found := strings.Cut(string(data), "### Example (synthetic)\n\n```yaml\n")
	example, _, closed := strings.Cut(after, "```")
	if !found || !closed {
		t.Fatal("hardware-catalog.md has no synthetic faceplate example")
	}
	var record map[string]any
	if err := yaml.Unmarshal([]byte(example), &record); err != nil {
		t.Fatal(err)
	}
	if errs := admit(t, "DeviceType", object(t, record), nil); len(errs) > 0 {
		t.Fatalf("documented example rejected: %v", errs)
	}
	spec := record["spec"].(map[string]any)
	if len(spec["faceplate"].(map[string]any)["banks"].([]any)) != 5 || len(spec["componentTemplates"].([]any)) != 7 {
		t.Fatal("example changed shape; update this test with it")
	}
}
