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
// # Mutually Exclusive Branch Groups
//
// When a protocol can proceed along exactly one of several paths (for
// example a successful handshake versus a degraded one), a BranchGroup
// declares the alternatives as ordered sequences of Call expectations
// instead of marking everything with Call.Maybe:
//
//	m := &mock.Mock{}
//	group := m.BranchGroup()
//
//	modern := group.Branch()
//	modern.On("Hello").Return("hi")
//	modern.On("Data", 1).Return("ok")
//
//	legacy := group.Branch()
//	legacy.On("Legacy").Return("old")
//	legacy.On("Data", 1).Return("ok")
//
// All branches start unselected. The first call that matches the first Call
// of a branch atomically selects that branch; later calls may only consume
// the selected branch's remaining expectations, in order. A call matching
// the heads of several branches at once fails with an explicit ambiguity
// error, and a call matching nothing fails with the usual unexpected call
// diagnostic, extended with the candidate branch heads.
//
// AssertExpectations only checks the remaining expectations of the selected
// branch; branches that were never selected are ignored. A group in which
// no branch was ever selected fails the assertion unless it was marked with
// BranchGroup.Optional.
//
// A 90-second example that prints the selection result and the calls that
// are still unmet:
//
//	m.MethodCalled("Legacy")               // selects branch 1
//	fmt.Println(group.Selected())          // 1
//	fmt.Println(group.UnmetExpectations()) // [Data(int)]
//	m.MethodCalled("Data", 1)
//	fmt.Println(group.UnmetExpectations()) // []
package mock
