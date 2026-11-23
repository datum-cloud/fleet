package v1alpha1

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ManufacturerSpec struct {
	PhysicalMetadata `json:",inline"`
	CanonicalName    string   `json:"canonicalName"`
	Aliases          []string `json:"aliases,omitempty"`
}

type ComponentTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Connector string `json:"connector,omitempty"`
	Positions int32  `json:"positions,omitempty"`
	// Face is the chassis face the component sits on, relative to the
	// chassis and never to the rack. Unset means unknown. A Device component
	// inherits it from the template whose ID equals the component ID; there
	// is no Device-level override.
	Face ChassisFace `json:"face,omitempty"`
	// Layout places the template in cells of one faceplate bank on its own
	// face. It needs Face and a DeviceType faceplate. Unset means the
	// template's position on the face is not recorded; it is never guessed.
	Layout *TemplateLayout `json:"layout,omitempty"`
}

// TemplateLayout is a nominal bank-grid position, never a millimetre
// coordinate. Row and Column are 1-based. Span covers that many columns from
// Column to the right; unset means 1.
type TemplateLayout struct {
	Bank   string `json:"bank"`
	Row    int32  `json:"row"`
	Column int32  `json:"column"`
	Span   int32  `json:"span,omitempty"`
}

// Faceplate is the nominal physical layout of a model's faces as clusters of
// identical cells, such as a 2x12 RJ45 block or a PSU bay. It is catalog data
// only: a Device inherits it through its DeviceType and cannot override it.
// It carries no state. Whether a port is cabled comes from Cable records.
type Faceplate struct {
	Banks []FaceplateBank `json:"banks"`
}

// FaceplateBank is one grid of cells on one chassis face. Offset is the
// bank's left edge as a fraction of the usable face width. On each face it is
// set on every bank or on none; with none, banks flow left to right in list
// order. CellMM sizes module cells, such as PSUs, drive bays and fans; unset
// means readers use the nominal size of the cell's connector.
type FaceplateBank struct {
	ID      string      `json:"id"`
	Face    ChassisFace `json:"face"`
	Rows    int32       `json:"rows"`
	Columns int32       `json:"columns"`
	Offset  *float64    `json:"offset,omitempty"`
	CellMM  *CellSize   `json:"cellMM,omitempty"`
}

// CellSize is a nominal cell size in millimetres.
type CellSize struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ChassisFace names a side of the chassis itself. The front is whatever the
// manufacturer calls the front; for many switches that is the port side.
type ChassisFace string

const (
	ChassisFront ChassisFace = "Front"
	ChassisRear  ChassisFace = "Rear"
)

type DeviceTypeSpec struct {
	PhysicalMetadata   `json:",inline"`
	ManufacturerRef    LocalReference      `json:"manufacturerRef"`
	Model              string              `json:"model"`
	ModelShape         *ModelShape         `json:"modelShape,omitempty"`
	ComponentTemplates []ComponentTemplate `json:"componentTemplates,omitempty"`
	// Faceplate is optional. Unset means the layout is not recorded, and
	// readers show grouped components instead of a drawing.
	Faceplate *Faceplate `json:"faceplate,omitempty"`
}

type Manufacturer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ManufacturerSpec `json:"spec"`
}

type ManufacturerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Manufacturer `json:"items"`
}

type DeviceType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeviceTypeSpec `json:"spec"`
}

type DeviceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceType `json:"items"`
}

func (in *Manufacturer) DeepCopyObject() runtime.Object {
	out := new(Manufacturer)
	deepCopy(in, out)
	return out
}

func (in *ManufacturerList) DeepCopyObject() runtime.Object {
	out := new(ManufacturerList)
	deepCopy(in, out)
	return out
}

func (in *DeviceType) DeepCopyObject() runtime.Object {
	out := new(DeviceType)
	deepCopy(in, out)
	return out
}

func (in *DeviceTypeList) DeepCopyObject() runtime.Object {
	out := new(DeviceTypeList)
	deepCopy(in, out)
	return out
}

func ReadDeviceCatalog(ctx context.Context, reader client.Reader, device *Device) (*DeviceType, *Manufacturer, error) {
	unavailable := fmt.Errorf("hardware catalog unavailable")
	if reader == nil || device == nil || device.Namespace == "" || !device.DeletionTimestamp.IsZero() || device.Spec.DeviceTypeRef == nil || device.Spec.DeviceTypeRef.Name == "" {
		return nil, nil, unavailable
	}
	var model DeviceType
	if err := reader.Get(ctx, client.ObjectKey{Namespace: device.Namespace, Name: device.Spec.DeviceTypeRef.Name}, &model); err != nil || !model.DeletionTimestamp.IsZero() || model.Spec.ManufacturerRef.Name == "" {
		return nil, nil, unavailable
	}
	var manufacturer Manufacturer
	if err := reader.Get(ctx, client.ObjectKey{Namespace: device.Namespace, Name: model.Spec.ManufacturerRef.Name}, &manufacturer); err != nil || !manufacturer.DeletionTimestamp.IsZero() {
		return nil, nil, unavailable
	}
	return &model, &manufacturer, nil
}
