// Package mock provides a system by which it is possible to mock your objects
// and verify calls are happening as expected.
//
// # Example Usage
//
// The mock package provides an object, Mock, that tracks activity on another object.  It is usually
// embedded into a test object as shown below:
//
//	type MyTestObject struct {
//	  // add a Mock object instance
//	  mock.Mock
//
//	  // other fields go here as normal
//	}
//
// When implementing the methods of an interface, you wire your functions up
// to call the Mock.Called(args...) method, and return the appropriate values.
//
// For example, to mock a method that saves the name and age of a person and returns
// the year of their birth or an error, you might write this:
//
//	func (o *MyTestObject) SavePersonDetails(firstname, lastname string, age int) (int, error) {
//	  args := o.Called(firstname, lastname, age)
//	  return args.Int(0), args.Error(1)
//	}
//
// The Int, Error and Bool methods are examples of strongly typed getters that take the argument
// index position. Given this argument list:
//
//	(12, true, "Something")
//
// You could read them out strongly typed like this:
//
//	args.Int(0)
//	args.Bool(1)
//	args.String(2)
//
// For objects of your own type, use the generic Arguments.Get(index) method and make a type assertion:
//
//	return args.Get(0).(*MyObject), args.Get(1).(*AnotherObjectOfMine)
//
// This may cause a panic if the object you are getting is nil (the type assertion will fail), in those
// cases you should check for nil first.
//
// # Phased Scenarios
//
// When a test needs stronger ordering guarantees than [Call.NotBefore], a
// [Scenario] groups expectations into named, strictly ordered phases:
//
//	scenario := mock.NewScenario(&m.Mock)
//	scenario.Phase("stage1")
//	scenario.On("A").Return().Twice()
//	scenario.On("B").Return().Once()
//	scenario.Phase("stage2")
//	scenario.On("Commit").Return().Once()
//	scenario.Freeze()
//
// Calls within one phase may happen in any order, but a call belonging to a
// later phase is rejected until every required call of the current phase has
// been made. Optional expectations (see [Call.Maybe]) never block phase
// advancement, and once a phase is closed its optional expectations can no
// longer be consumed. Expectations registered through [Scenario.On] are
// regular [Mock.On] expectations, so Return, Times, NotBefore and the other
// [Call] modifiers keep working, and plain [Mock.On] expectations outside
// the scenario stay ungated.
//
// Once registration is complete, [Scenario.Freeze] seals the scenario.
// Scenario failures report only the phase name, the method name and the
// sequence number of the actual call; argument formatting remains governed
// by the regular [Mock] formatting policy.
package mock
