// Package change reads APEX v0's change-routes table
// (_apex/change-routes.yaml) for `ape doctor`'s framework.change_routes row
// and the framework contract gates. The v0 maintenance lane it once drove
// (`ape change` before APEX v1 gave the name to the unit of change) is gone;
// what remains of its contract and layout types serves the frameworkcontract
// tests.
package change
