package config

import (
	"fmt"
	"reflect"
)

// clonePlatformConfig returns a fully detached copy, including values nested
// inside option maps. PlatformConfig is acyclic by construction.
func clonePlatformConfig(cfg *PlatformConfig) (*PlatformConfig, error) {
	if cfg == nil {
		return nil, nil
	}
	cloned, err := cloneConfigValue(reflect.ValueOf(cfg))
	if err != nil {
		return nil, fmt.Errorf("clone config: %w", err)
	}
	return cloned.Interface().(*PlatformConfig), nil
}

func cloneConfigValue(value reflect.Value) (reflect.Value, error) {
	if !value.IsValid() {
		return value, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneConfigValue(value.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloned)
		return out, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneConfigValue(value.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloned)
		return out, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			key, err := cloneConfigValue(iter.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			item, err := cloneConfigValue(iter.Value())
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(key, item)
		}
		return out, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			item, err := cloneConfigValue(value.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(item)
		}
		return out, nil
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			item, err := cloneConfigValue(value.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(item)
		}
		return out, nil
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if !out.Field(i).CanSet() {
				return reflect.Value{}, fmt.Errorf("unsupported unexported field %s", value.Type().Field(i).Name)
			}
			item, err := cloneConfigValue(value.Field(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Field(i).Set(item)
		}
		return out, nil
	default:
		return value, nil
	}
}
