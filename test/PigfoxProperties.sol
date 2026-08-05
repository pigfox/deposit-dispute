// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

/// @title PigfoxProperties — the property-harness base, re-implemented here.
/// @notice Every pigfox contract repo has exactly one `test/Properties.sol` holding a
///         contract `Properties is PigfoxProperties`. Foundry, Echidna and Medusa all drive
///         THAT contract, so an invariant can never hold in one engine and quietly rot in
///         another.
///
/// @dev    WHY THIS BASE EXISTS AT ALL.
///         A property harness has a failure mode worse than a failing property: a property
///         that stops being *registered*. A rename, a stale `crytic-export/`, artifacts left
///         behind by `forge coverage`, a `testPrefixes` mismatch — any of these makes a
///         fuzzer walk a smaller property set and report a green run. It has happened in
///         this estate: a stale artifact once shrank a five-property set to four and the job
///         passed. Green on four is not green.
///
///         So the count is declared in Solidity, and independent things must agree with it
///         before a build passes: this declaration, the static count of `function echidna_`
///         declarations in `test/Properties.sol`, and what each fuzzer actually REGISTERED
///         at runtime.
///
/// @dev    RE-IMPLEMENTED, NOT VENDORED, AND THAT IS A DELIBERATE COST. The estate's shared
///         copy lives in `pigfox/solidity-pipeline` and is normally consumed as a git
///         submodule so a fix lands once instead of four times. This repo is required to
///         clone and build with no pigfox dependency of any kind, so it carries its own
///         copy and accepts the drift risk that comes with it. The obligation this creates
///         is explicit: when this repo joins the shared pipeline, this file is DELETED and
///         the import re-pointed — it is not reconciled, merged, or kept alongside.
///
/// @dev    NO DEPENDENCIES, DELIBERATELY. This file imports nothing — not forge-std, not
///         OpenZeppelin. A base contract that dragged in a second copy of forge-std would
///         create exactly the duplicate-artifact confusion the count guard exists to catch.
abstract contract PigfoxProperties {
    /// @notice How many `echidna_*` predicates this harness declares.
    /// @dev    Override with a literal, not a computed expression. The whole value of this
    ///         number is that it is an independent statement of intent — deriving it from
    ///         anything the harness already knows would make it agree with a broken harness
    ///         automatically.
    ///
    ///         `pure` and prefixed `pigfox`, not `echidna_`, so no fuzzer ever mistakes the
    ///         declaration for one of the properties it counts.
    /// @return The number of `echidna_*` predicates in this contract.
    function pigfoxPropertyCount() public pure virtual returns (uint256);

    /// @notice A one-line statement of what this harness is proving.
    /// @dev    Read into the CI log by the pipeline so a run says what it covered rather
    ///         than only how many things it covered. Override it; the default is
    ///         deliberately useless so an unoverridden harness reads as unfinished rather
    ///         than as fine.
    /// @return A short human-readable description of the harness.
    function pigfoxHarnessDescription() public pure virtual returns (string memory) {
        return "UNDESCRIBED HARNESS";
    }
}
