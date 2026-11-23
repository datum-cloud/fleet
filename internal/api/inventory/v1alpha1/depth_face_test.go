package v1alpha1

import "testing"

// Unset depth stays unset on both kinds: a default would hide "depth
// unknown" and make conflict checks overconfident.
func TestModelShapeDepthIsNeverDefaulted(t *testing.T) {
	_, model := catalogRecords()
	half, height := false, 1.0
	for _, test := range []struct {
		name  string
		shape *ModelShape
		want  any
	}{
		{"no model shape", nil, nil},
		{"shape without depth", &ModelShape{HeightUnits: &height}, nil},
		{"explicit half depth", &ModelShape{FullDepth: &half}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := model.DeepCopyObject().(*DeviceType)
			candidate.Spec.ModelShape = test.shape
			device := syntheticDevice()
			device.Spec.ModelShape = test.shape
			for kind, admitted := range map[string]map[string]any{"DeviceType": object(t, candidate), "Device": object(t, device)} {
				if errs := admit(t, kind, admitted, nil); len(errs) > 0 {
					t.Fatal(kind, errs)
				}
				shape, ok := admitted["spec"].(map[string]any)["modelShape"].(map[string]any)
				if test.shape == nil {
					if ok {
						t.Fatalf("%s modelShape was defaulted: %v", kind, shape)
					}
					continue
				}
				if got := shape["fullDepth"]; got != test.want {
					t.Fatalf("%s admitted fullDepth = %v, want %v", kind, got, test.want)
				}
			}
		})
	}
}

func TestComponentTemplateFaceIsChassisRelativeEnum(t *testing.T) {
	_, model := catalogRecords()
	for _, face := range []ChassisFace{ChassisFront, ChassisRear, ""} {
		candidate := model.DeepCopyObject().(*DeviceType)
		candidate.Spec.ComponentTemplates[0].Face = face
		if errs := admit(t, "DeviceType", object(t, candidate), nil); len(errs) > 0 {
			t.Fatalf("face %q rejected: %v", face, errs)
		}
	}
	for _, face := range []string{"front", "rear", "Left", "Unknown", ""} {
		candidate := object(t, model)
		templates := candidate["spec"].(map[string]any)["componentTemplates"].([]any)
		templates[0].(map[string]any)["face"] = face
		if errs := admit(t, "DeviceType", candidate, nil); len(errs) == 0 {
			t.Fatalf("face %q accepted; template faces are Front or Rear, and unknown is omitted", face)
		}
	}
}

// The mounted rack face keeps its lowercase vocabulary. The chassis face
// vocabulary is not accepted there, so the two cannot be confused.
func TestMountedRackFaceRejectsChassisVocabulary(t *testing.T) {
	for face, valid := range map[string]bool{"front": true, "rear": true, "Front": false, "Rear": false} {
		device := reportedDevice()
		device.Spec.ReportedPlacement.Face = face
		if errs := admit(t, "Device", object(t, device), nil); (len(errs) == 0) != valid {
			t.Fatalf("reported placement face %q: valid=%v, errors %v", face, valid, errs)
		}
	}
}
