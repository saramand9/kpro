package gormstore

import (
	"reflect"

	"gorm.io/gorm/schema"
)

type tableNamer interface {
	TableName() string
}

func modelTableName[T any]() string {
	var zero T
	modelType := reflect.TypeOf(zero)
	if modelType == nil {
		return ""
	}
	for modelType.Kind() == reflect.Pointer {
		modelType = modelType.Elem()
	}
	if modelType.Kind() != reflect.Struct {
		return ""
	}
	if model, ok := reflect.New(modelType).Elem().Interface().(tableNamer); ok {
		if table := model.TableName(); table != "" {
			return table
		}
	}
	if model, ok := reflect.New(modelType).Interface().(tableNamer); ok {
		if table := model.TableName(); table != "" {
			return table
		}
	}
	return schema.NamingStrategy{}.TableName(modelType.Name())
}
