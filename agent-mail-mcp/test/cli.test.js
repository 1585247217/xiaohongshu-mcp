import test from "node:test";
import assert from "node:assert/strict";
import { cliPath } from "../src/cli.js";

test("official CLI path is configured", () => assert.match(cliPath, /agently-cli$/));
