// Code generated by mockery v2.36.0. DO NOT EDIT.

package mocks

import mock "github.com/stretchr/testify/mock"

// Registrator is an autogenerated mock type for the Registrator type
type Registrator struct {
	mock.Mock
}

// DeregisterFromCompass provides a mock function with given fields: compassID, globalAccount
func (_m *Registrator) DeregisterFromCompass(compassID string, globalAccount string) error {
	ret := _m.Called(compassID, globalAccount)

	var r0 error
	if rf, ok := ret.Get(0).(func(string, string) error); ok {
		r0 = rf(compassID, globalAccount)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// RegisterInCompass provides a mock function with given fields: compassRuntimeLabels
func (_m *Registrator) RegisterInCompass(compassRuntimeLabels map[string]interface{}) (string, error) {
	ret := _m.Called(compassRuntimeLabels)

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(map[string]interface{}) (string, error)); ok {
		return rf(compassRuntimeLabels)
	}
	if rf, ok := ret.Get(0).(func(map[string]interface{}) string); ok {
		r0 = rf(compassRuntimeLabels)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(map[string]interface{}) error); ok {
		r1 = rf(compassRuntimeLabels)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewRegistrator creates a new instance of Registrator. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewRegistrator(t interface {
	mock.TestingT
	Cleanup(func())
}) *Registrator {
	mock := &Registrator{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
