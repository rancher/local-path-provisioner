/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package protosanitizer supports logging of gRPC messages without
// accidentally revealing sensitive fields.
package protosanitizer

import (
	"encoding/json"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// StripSecrets returns a wrapper around the original CSI gRPC message
// which has a Stringer implementation that serializes the message
// as one-line JSON, but without including secret information.
// Instead of the secret value(s), the string "***stripped***" is
// included in the result.
//
// StripSecrets relies on an extension in CSI 1.0 and thus can only
// be used for messages based on that or a more recent spec!
//
// StripSecrets itself is fast and therefore it is cheap to pass the
// result to logging functions which may or may not end up serializing
// the parameter depending on the current log level.
func StripSecrets(msg interface{}) fmt.Stringer {
	return &stripSecrets{msg}
}

type stripSecrets struct {
	msg any
}

func (s *stripSecrets) String() string {
	stripped := s.msg

	// also support scalar types like string, int, etc.
	msg, ok := s.msg.(proto.Message)
	if ok {
		stripped = stripMessage(msg.ProtoReflect())
	}

	b, err := json.Marshal(stripped)
	if err != nil {
		return fmt.Sprintf("<<json.Marshal %T: %s>>", s.msg, err)
	}
	return string(b)
}

func stripSingleValue(field protoreflect.FieldDescriptor, v protoreflect.Value) any {
	switch field.Kind() {
	case protoreflect.MessageKind:
		return stripMessage(v.Message())
	case protoreflect.EnumKind:
		desc := field.Enum().Values().ByNumber(v.Enum())
		if desc == nil {
			return v.Enum()
		}
		return desc.Name()
	default:
		return v.Interface()
	}
}

func stripValue(field protoreflect.FieldDescriptor, v protoreflect.Value) any {
	if field.IsList() {
		l := v.List()
		res := make([]any, l.Len())
		for i := range l.Len() {
			res[i] = stripSingleValue(field, l.Get(i))
		}
		return res
	} else if field.IsMap() {
		m := v.Map()
		res := make(map[string]any, m.Len())
		m.Range(func(mk protoreflect.MapKey, v protoreflect.Value) bool {
			res[mk.String()] = stripSingleValue(field.MapValue(), v)
			return true
		})
		return res
	} else {
		return stripSingleValue(field, v)
	}
}

func stripMessage(msg protoreflect.Message) map[string]any {
	stripped := make(map[string]any)

	// Walk through all fields and replace those with ***stripped*** that
	// are marked as secret.
	msg.Range(func(field protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := field.TextName()
		if isCSI1Secret(field) {
			stripped[name] = "***stripped***"
		} else {
			stripped[name] = stripValue(field, v)
		}
		return true
	})
	return stripped
}

// isCSI1Secret uses the csi.E_CsiSecret extension from CSI 1.0 to
// determine whether a field contains secrets.
func isCSI1Secret(desc protoreflect.FieldDescriptor) bool {
	ex := proto.GetExtension(desc.Options(), csi.E_CsiSecret)
	return ex.(bool)
}
