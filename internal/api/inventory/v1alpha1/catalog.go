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
}

type DeviceTypeSpec struct {
	PhysicalMetadata   `json:",inline"`
	ManufacturerRef    LocalReference      `json:"manufacturerRef"`
	Model              string              `json:"model"`
	ModelShape         *ModelShape         `json:"modelShape,omitempty"`
	ComponentTemplates []ComponentTemplate `json:"componentTemplates,omitempty"`
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
