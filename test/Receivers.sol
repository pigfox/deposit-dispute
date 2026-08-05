// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import {DepositDispute} from "../src/DepositDispute.sol";

/// @title RejectingReceiver — a party that refuses its own money.
/// @notice Stands in as the landlord or the tenant so the failed-transfer path in
///         {DepositDispute.withdraw} is reachable. Without a payee that can refuse, that
///         revert is unreachable code and the credit path would be tested only where it
///         succeeds.
contract RejectingReceiver {
    /// @notice Forwards `data` to `target` and reports whether it succeeded.
    /// @param target The contract to call.
    /// @param data The calldata to send.
    /// @return ok Whether the call succeeded.
    /// @return ret The raw return data.
    function call(address target, bytes calldata data) external returns (bool ok, bytes memory ret) {
        // solhint-disable-next-line avoid-low-level-calls
        (ok, ret) = target.call(data);
    }

    /// @notice Refuses every incoming transfer.
    receive() external payable {
        revert("no thanks");
    }
}

/// @title ReenteringParty — a payee that calls back in during its own withdrawal.
/// @notice The only way to reach {DepositDispute}'s reentrancy guard. The contract's single
///         external call is the transfer inside `withdraw`, so a re-entry can only originate
///         in a payee's fallback — and re-entering `withdraw` itself would find a zeroed
///         balance and be refused by CEI before the guard was consulted. So this re-enters
///         `settle`, which is guarded for exactly this reason.
///
/// @dev    The revert is CAUGHT rather than bubbled. Left to bubble it would make the
///         transfer fail and `withdraw` revert with `TransferFailed`, which is a different
///         error about a different thing — the test would then pass while proving nothing
///         about the guard. Catching it lets the withdrawal succeed AND records the selector
///         the guard actually produced.
contract ReenteringParty {
    /// @notice The dispute to re-enter. Set once, after deployment, because the dispute's
    ///         constructor needs this contract's address.
    DepositDispute public dispute;
    /// @notice Whether the re-entrant call was attempted at all.
    bool public reentryAttempted;
    /// @notice Whether the re-entrant call SUCCEEDED. Must stay false.
    bool public reentrySucceeded;
    /// @notice The raw revert data the guard produced.
    bytes public reentryError;

    /// @notice Binds the dispute this party will re-enter.
    /// @param d The dispute contract.
    function bind(DepositDispute d) external {
        dispute = d;
    }

    /// @notice Forwards `data` to `target` and reports whether it succeeded.
    /// @param target The contract to call.
    /// @param data The calldata to send.
    /// @return ok Whether the call succeeded.
    /// @return ret The raw return data.
    function call(address target, bytes calldata data) external returns (bool ok, bytes memory ret) {
        // solhint-disable-next-line avoid-low-level-calls
        (ok, ret) = target.call(data);
    }

    /// @notice Accepts the payout and immediately tries to re-enter `settle`.
    receive() external payable {
        reentryAttempted = true;
        try dispute.settle() {
            reentrySucceeded = true;
        } catch (bytes memory err) {
            reentryError = err;
        }
    }
}
