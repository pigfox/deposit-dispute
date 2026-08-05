// SPDX-License-Identifier: MIT
pragma solidity 0.8.28;

import {Script} from "forge-std/Script.sol";
import {console} from "forge-std/console.sol";

import {DepositDispute} from "../src/DepositDispute.sol";

/// @title Deploy — the two seeded disputes, to Base Sepolia 84532.
/// @notice DIRECT-CHAIN ONLY. This script deploys to the endpoint the environment names and
///         nothing else. There is no local-node path, no in-memory substitute standing in for
///         a deployment, and no rehearsal mode: a deployment either happened on 84532 or it
///         did not happen.
///
/// @dev WHY TWO DISPUTES AND NOT ONE. A dispute settles exactly once, so it can demonstrate a
///      PARTIAL SPLIT or the CAP, never both. The showpiece is the pair, so the pair is what
///      gets deployed.
///
///      DISPUTE A — THE SPLIT. The schedule sums to 0.0019 ether against a 0.002 ether
///      deposit, so the claim cannot reach the deposit even if the panel establishes every
///      single item. Whatever the three models decide, as long as they establish at least one
///      item, BOTH parties are credited something. The only outcome that is not a split is
///      the panel establishing nothing at all — which is a real possible answer and would be
///      reported as one, not re-run until it went away.
///
///      DISPUTE B — THE CAP. Every item claims 0.0003 ether against a 0.0002 ether deposit,
///      so ANY single established item already puts the claim over the deposit. The landlord
///      is credited the deposit and no more, and the excess is not recorded as a debt
///      anywhere. One established item is enough; five is the same answer.
///
/// @dev THE MODELS AND THE SIGNERS COME FROM THE ENVIRONMENT, not from literals here. They
///      are what the adjudicator will be configured with, and `internal/chain` reads them
///      back off chain and refuses to run if the two disagree. Writing them twice — once here
///      and once in the service — is exactly the drift that check exists to catch, so they
///      are written once, in the environment both read.
contract Deploy is Script {
    /*//////////////////////////////////////////////////////////////
                           DISPUTE A — THE SPLIT
    //////////////////////////////////////////////////////////////*/

    /// @notice The deposit held by dispute A, in wei.
    uint256 public constant SPLIT_DEPOSIT = 0.002 ether;

    uint256 public constant SPLIT_CARPET = 0.0002 ether;
    uint256 public constant SPLIT_WALL = 0.0003 ether;
    uint256 public constant SPLIT_WINDOW = 0.0004 ether;
    uint256 public constant SPLIT_DOOR = 0.0005 ether;
    uint256 public constant SPLIT_CLEANING = 0.0005 ether;

    /*//////////////////////////////////////////////////////////////
                            DISPUTE B — THE CAP
    //////////////////////////////////////////////////////////////*/

    /// @notice The deposit held by dispute B, in wei. Smaller than any single claimed item.
    uint256 public constant CAP_DEPOSIT = 0.0002 ether;
    /// @notice What every item of dispute B claims. Above CAP_DEPOSIT on purpose.
    uint256 public constant CAP_ITEM = 0.0003 ether;

    /*//////////////////////////////////////////////////////////////
                              DESCRIPTIONS
    //////////////////////////////////////////////////////////////*/

    // THESE STRINGS ARE THE OTHER HALF OF THE PUBLISHED BUNDLES. The contract stores
    // keccak256 of each one, and the adjudicator shows the same text to the models. If the
    // two ever disagree, the models would be answering about a deduction the chain does not
    // describe. TestTheDeployedScheduleMatchesThePublishedBundles holds them together.

    string public constant SPLIT_DESC_CARPET = "Carpet, main bedroom: staining beyond fair wear and tear";
    string public constant SPLIT_DESC_WALL = "Wall, living room: unfilled fixings";
    string public constant SPLIT_DESC_WINDOW = "Window, second bedroom: cracked pane";
    string public constant SPLIT_DESC_DOOR = "Front door lock: replacement";
    string public constant SPLIT_DESC_CLEANING = "End-of-tenancy cleaning";

    string public constant CAP_DESC_KITCHEN = "Kitchen: fire damage to units and worktop";
    string public constant CAP_DESC_FLOOR = "Living room floor: water damage through to subfloor";
    string public constant CAP_DESC_BATHROOM = "Bathroom: removed and unreturned fittings";
    string public constant CAP_DESC_GARDEN = "Garden: removal of dumped material";
    string public constant CAP_DESC_REDECORATION = "Whole property: redecoration after unauthorized painting";

    /*//////////////////////////////////////////////////////////////
                                  RUN
    //////////////////////////////////////////////////////////////*/

    /// @notice Deploys both disputes and prints their addresses.
    /// @dev The deposits are sent from the broadcasting key, because a DepositDispute takes
    ///      custody at construction and has no later funding path. That is deliberate — see
    ///      the contract's constructor — and it means the deployer must hold
    ///      SPLIT_DEPOSIT + CAP_DEPOSIT plus gas.
    function run() external {
        address landlord = vm.envAddress("DD_LANDLORD_ADDR");
        address tenant = vm.envAddress("DD_TENANT_ADDR");

        address[3] memory signers = [
            vm.envAddress("DD_SLOT0_SIGNER_ADDR"),
            vm.envAddress("DD_SLOT1_SIGNER_ADDR"),
            vm.envAddress("DD_SLOT2_SIGNER_ADDR")
        ];
        string[3] memory modelIds = [
            vm.envString("DD_SLOT0_MODEL_ID"),
            vm.envString("DD_SLOT1_MODEL_ID"),
            vm.envString("DD_SLOT2_MODEL_ID")
        ];

        vm.startBroadcast();

        DepositDispute split = new DepositDispute{
            value: SPLIT_DEPOSIT
        }(landlord, tenant, splitDescHashes(), splitAmounts(), signers, modelIds);
        DepositDispute cap = new DepositDispute{
            value: CAP_DEPOSIT
        }(landlord, tenant, capDescHashes(), capAmounts(), signers, modelIds);

        vm.stopBroadcast();

        console.log("DISPUTE A (split) :", address(split));
        console.log("DISPUTE B (cap)   :", address(cap));
        console.log("landlord          :", landlord);
        console.log("tenant            :", tenant);
    }

    /*//////////////////////////////////////////////////////////////
                                SCHEDULES
    //////////////////////////////////////////////////////////////*/

    /// @notice Dispute A's description hashes, in schedule order.
    function splitDescHashes() public pure returns (bytes32[5] memory d) {
        d[0] = keccak256(bytes(SPLIT_DESC_CARPET));
        d[1] = keccak256(bytes(SPLIT_DESC_WALL));
        d[2] = keccak256(bytes(SPLIT_DESC_WINDOW));
        d[3] = keccak256(bytes(SPLIT_DESC_DOOR));
        d[4] = keccak256(bytes(SPLIT_DESC_CLEANING));
    }

    /// @notice Dispute A's claimed amounts, in schedule order.
    function splitAmounts() public pure returns (uint256[5] memory a) {
        a[0] = SPLIT_CARPET;
        a[1] = SPLIT_WALL;
        a[2] = SPLIT_WINDOW;
        a[3] = SPLIT_DOOR;
        a[4] = SPLIT_CLEANING;
    }

    /// @notice Dispute B's description hashes, in schedule order.
    function capDescHashes() public pure returns (bytes32[5] memory d) {
        d[0] = keccak256(bytes(CAP_DESC_KITCHEN));
        d[1] = keccak256(bytes(CAP_DESC_FLOOR));
        d[2] = keccak256(bytes(CAP_DESC_BATHROOM));
        d[3] = keccak256(bytes(CAP_DESC_GARDEN));
        d[4] = keccak256(bytes(CAP_DESC_REDECORATION));
    }

    /// @notice Dispute B's claimed amounts, in schedule order. Every one exceeds the deposit.
    function capAmounts() public pure returns (uint256[5] memory a) {
        for (uint256 i = 0; i < a.length; i++) {
            a[i] = CAP_ITEM;
        }
    }
}
