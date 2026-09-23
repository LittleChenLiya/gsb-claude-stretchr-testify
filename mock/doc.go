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
// # Mutually exclusive branch groups
//
// When a protocol can proceed down exactly one of several paths (for example
// a modern handshake versus a legacy fallback), a [BranchGroup] declares the
// alternatives instead of marking every call with [Call.Maybe]:
//
//	group := m.NewBranchGroup()
//	modern := group.Branch()
//	modern.On("Hello").Return("hi")
//	modern.On("Data", 1).Return("ok")
//	legacy := group.Branch()
//	legacy.On("Legacy").Return("old")
//	legacy.On("Data", 2).Return("done")
//
// Each branch holds an ordered sequence of [Call] expectations. All branches
// start unselected; the first incoming call that matches the first Call of a
// branch atomically selects that branch, and later calls can only consume the
// remaining expectations of the selected branch. A call that matches the
// first Call of several branches fails with an ambiguity error, and a call
// that matches nothing fails with the usual unexpected call diagnostic, now
// listing the candidate branches.
//
// [Mock.AssertExpectations] only checks the remaining expectations of the
// selected branch; branches that were never selected are not required. A
// group that was never entered fails the assertion by default, unless it was
// marked optional with [BranchGroup.Optional]. [BranchGroup.SelectedBranch]
// reports which branch was selected, or -1 if none was.
package mock
