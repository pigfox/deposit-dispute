// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

/// @title Actor — a callable stand-in for a distinct party.
/// @notice Echidna and Medusa have no cheatcodes, so a harness that needs a call to arrive
///         from a specific `msg.sender` cannot prank one: it has to own a contract at that
///         address and call through it. That is all this is.
///
/// @dev    It matters here three times over. {DepositDispute.fileClaim} accepts only the
///         landlord, {DepositDispute.submitVerdict} only a registered adjudicator, and
///         {DepositDispute.withdraw} pays only whoever calls it — so without distinct actors
///         a campaign could file no claim, cast no verdict and take no payout, and every
///         property about adjudication and custody would hold vacuously over a system
///         nothing had driven.
contract Actor {
    /// @notice Forwards `data` to `target` and reports whether it succeeded.
    /// @dev Returns the outcome rather than bubbling the revert. The harness compares
    ///      outcomes against what it independently expected; a bubbled revert would abort
    ///      the fuzzer's transaction instead, turning an interesting state into a discarded
    ///      one.
    /// @param target The contract to call.
    /// @param data The calldata to send.
    /// @return ok Whether the call succeeded.
    /// @return ret The raw return data.
    function call(address target, bytes calldata data) external returns (bool ok, bytes memory ret) {
        // solhint-disable-next-line avoid-low-level-calls
        (ok, ret) = target.call(data);
    }

    /// @notice Forwards `data` to `target` with `value` attached.
    /// @dev Separate from {call} so the harness cannot fund a dispute by accident: the only
    ///      value-bearing call in this system is the constructor, and the harness makes it
    ///      directly rather than through an actor.
    /// @param target The contract to call.
    /// @param value The wei to attach.
    /// @param data The calldata to send.
    /// @return ok Whether the call succeeded.
    /// @return ret The raw return data.
    function callWithValue(address target, uint256 value, bytes calldata data)
        external
        returns (bool ok, bytes memory ret)
    {
        // solhint-disable-next-line avoid-low-level-calls
        (ok, ret) = target.call{value: value}(data);
    }

    /// @notice Accepts ether, so a party actor can be paid.
    /// @dev Without this, every withdrawal in a campaign would revert on transfer and the
    ///      payout path would be unreachable — which the solvency and conservation
    ///      properties would then hold over vacuously.
    receive() external payable {}
}
