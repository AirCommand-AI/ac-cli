import {
	chmodSync,
	closeSync,
	fstatSync,
	mkdirSync,
	openSync,
	readFileSync,
	readSync,
	watch,
	type FSWatcher,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const WORKSTREAM_FLAG = "aircommand-workstream";
const AGENT_FLAG = "aircommand-agent";
const CLI_FLAG = "aircommand-cli";
const COMMAND_NAME = "aircommand";
const CONNECT_TOOL_NAME = "aircommand_connect";
const ENCODED_COMPONENT_PREFIX = "id-";
const SAFE_FILENAME_COMPONENT = /^[A-Za-z0-9._-]+$/;

interface Enrollment {
	workstreamCode: string;
	agentId: string;
}

interface StoredCredential {
	workstreamCode?: unknown;
	agentId?: unknown;
}

interface CredentialFile {
	version?: unknown;
	agents?: unknown;
}

interface SpoolNotification {
	type?: unknown;
	updateId?: unknown;
	summary?: unknown;
}

interface TailHandle {
	close(): void;
}

interface ActiveConnection {
	enrollment: Enrollment;
	tail: TailHandle;
	token: object;
}

export default function aircommandExtension(pi: ExtensionAPI) {
	pi.registerFlag(WORKSTREAM_FLAG, {
		description: "AirCommand workstream code",
		type: "string",
	});
	pi.registerFlag(AGENT_FLAG, {
		description: "AirCommand agent ID",
		type: "string",
	});
	pi.registerFlag(CLI_FLAG, {
		description: "Path to ac-cli used in injected read guidance",
		type: "string",
		default: join(homedir(), ".local", "bin", "ac-cli"),
	});

	let activeConnection: ActiveConnection | undefined;
	let sessionActive = false;

	const disconnect = (): Enrollment | undefined => {
		const connection = activeConnection;
		activeConnection = undefined;
		connection?.tail.close();
		return connection?.enrollment;
	};

	const connect = (enrollment: Enrollment, ctx: ExtensionContext): "connected" | "already-connected" => {
		if (!sessionActive) {
			throw new Error("AirCommand cannot connect before the pi session starts.");
		}
		if (
			activeConnection?.enrollment.agentId === enrollment.agentId &&
			activeConnection.enrollment.workstreamCode === enrollment.workstreamCode
		) {
			return "already-connected";
		}

		const token = {};
		const cliPath = expandHome(readStringFlag(pi, CLI_FLAG) ?? join(homedir(), ".local", "bin", "ac-cli"));
		const readCommand = formatReadCommand(cliPath, enrollment);
		const newTail = tailSpool(
			spoolPath(enrollment.agentId),
			(summary, notification) => {
				if (!sessionActive || activeConnection?.token !== token) return;
				pi.sendMessage(
					{
						customType: "aircommand-notification",
						content: [
							`[AirCommand] ${summary}`,
							"This notification is a pointer, not message content. Fetch current workstream detail before acting:",
							readCommand,
						].join("\n"),
						display: true,
						details: {
							workstreamCode: enrollment.workstreamCode,
							agentId: enrollment.agentId,
							type: typeof notification.type === "string" ? notification.type : undefined,
							updateId: typeof notification.updateId === "string" ? notification.updateId : undefined,
						},
					},
					{ deliverAs: "followUp", triggerTurn: true },
				);
			},
			(message) => {
				if (sessionActive && activeConnection?.token === token) notify(ctx, message, "warning");
			},
		);

		const previous = activeConnection;
		activeConnection = { enrollment, tail: newTail, token };
		try {
			previous?.tail.close();
		} catch {
			notify(ctx, "AirCommand could not close the previous notification spool watcher.", "warning");
		}
		return "connected";
	};

	pi.registerTool({
		name: CONNECT_TOOL_NAME,
		label: "Connect AirCommand",
		description:
			"Connect this running pi session to the AirCommand agent identified by an agent ID returned from ac-cli exchange. Starts watching only that agent's notification spool.",
		promptSnippet: "Connect this running session to a freshly enrolled AirCommand agent",
		promptGuidelines: [
			"Use aircommand_connect immediately after ac-cli exchange succeeds, passing the exact Agent ID from its output.",
		],
		parameters: Type.Object({
			agentId: Type.String({ minLength: 1, description: "Exact agent ID printed by ac-cli exchange" }),
		}),
		executionMode: "sequential",
		async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
			const enrollment = enrollmentFromCredential(params.agentId);
			const result = connect(enrollment, ctx);
			const message =
				result === "already-connected"
					? `AirCommand is already connected to agent ${enrollment.agentId} in workstream ${enrollment.workstreamCode}.`
					: `AirCommand connected to agent ${enrollment.agentId} in workstream ${enrollment.workstreamCode}.`;
			return {
				content: [{ type: "text", text: message }],
				details: { status: result, agentId: enrollment.agentId, workstreamCode: enrollment.workstreamCode },
			};
		},
	});

	pi.registerCommand(COMMAND_NAME, {
		description: "Connect or disconnect AirCommand: /aircommand connect <agentId> | /aircommand disconnect",
		handler: async (args, ctx) => {
			const parts = args.trim().split(/\s+/).filter(Boolean);
			if (parts[0] === "connect" && parts.length === 2) {
				try {
					const enrollment = enrollmentFromCredential(parts[1]);
					const result = connect(enrollment, ctx);
					notify(
						ctx,
						result === "already-connected"
							? `AirCommand is already watching workstream ${enrollment.workstreamCode} for agent ${enrollment.agentId}.`
							: `AirCommand is watching workstream ${enrollment.workstreamCode} for agent ${enrollment.agentId}.`,
						"info",
					);
				} catch (error) {
					notify(ctx, errorMessage(error), "error");
				}
				return;
			}
			if (parts[0] === "disconnect" && parts.length === 1) {
				try {
					const enrollment = disconnect();
					notify(
						ctx,
						enrollment
							? `AirCommand disconnected agent ${enrollment.agentId} from this pi session.`
							: "AirCommand is not connected in this pi session.",
						"info",
					);
				} catch (error) {
					notify(ctx, errorMessage(error), "error");
				}
				return;
			}
			notify(ctx, "Usage: /aircommand connect <agentId> | /aircommand disconnect", "warning");
		},
	});

	pi.on("session_start", async (_event, ctx) => {
		sessionActive = true;
		try {
			disconnect();
		} catch {
			// A stale session resource must not make an unrelated session fail to start.
		}

		const requestedWorkstream = readStringFlag(pi, WORKSTREAM_FLAG);
		const requestedAgent = readStringFlag(pi, AGENT_FLAG);
		if (!requestedWorkstream && !requestedAgent) return;

		try {
			if (!requestedAgent) {
				throw new Error(`AirCommand --${AGENT_FLAG} is required when --${WORKSTREAM_FLAG} is supplied.`);
			}
			const enrollment = requestedWorkstream
				? validateEnrollment({ workstreamCode: requestedWorkstream, agentId: requestedAgent })
				: enrollmentFromCredential(requestedAgent);
			connect(enrollment, ctx);
			notify(
				ctx,
				`AirCommand is watching workstream ${enrollment.workstreamCode} for agent ${enrollment.agentId}.`,
				"info",
			);
		} catch (error) {
			notify(ctx, errorMessage(error), "error");
		}
	});

	pi.on("session_shutdown", async () => {
		sessionActive = false;
		try {
			disconnect();
		} catch {
			// Session shutdown remains best-effort and idempotent.
		}
	});
}

function enrollmentFromCredential(agentIDInput: string): Enrollment {
	const agentId = agentIDInput.trim();
	if (!agentId) throw new Error("AirCommand agent ID is missing.");

	const path = join(agentDirectory(agentId), "credentials.json");
	let parsed: CredentialFile;
	try {
		parsed = JSON.parse(readFileSync(path, "utf8")) as CredentialFile;
	} catch {
		throw new Error(`AirCommand enrollment for agent ${safeDisplay(agentId)} is unavailable. Re-enroll the agent and try again.`);
	}
	if (parsed.version !== 1 || !isRecord(parsed.agents)) {
		throw new Error(`AirCommand enrollment for agent ${safeDisplay(agentId)} is invalid. Re-enroll the agent and try again.`);
	}

	const entries = Object.entries(parsed.agents);
	if (entries.length !== 1 || entries[0][0] !== agentId || !isRecord(entries[0][1])) {
		throw new Error(`AirCommand enrollment for agent ${safeDisplay(agentId)} is invalid. Re-enroll the agent and try again.`);
	}
	const stored = entries[0][1] as StoredCredential;
	if (stored.agentId !== agentId || typeof stored.workstreamCode !== "string") {
		throw new Error(`AirCommand enrollment for agent ${safeDisplay(agentId)} is invalid. Re-enroll the agent and try again.`);
	}
	return validateEnrollment({ workstreamCode: stored.workstreamCode, agentId });
}

function validateEnrollment(enrollment: Enrollment): Enrollment {
	if (!/^[A-Za-z0-9_-]+$/.test(enrollment.workstreamCode)) {
		throw new Error("AirCommand workstream code is invalid.");
	}
	if (!enrollment.agentId) {
		throw new Error("AirCommand agent ID is missing.");
	}
	return enrollment;
}

function agentDirectory(agentId: string): string {
	return join(homedir(), ".aircommand", "agents", filenameComponent(agentId));
}

function spoolPath(agentId: string): string {
	return join(agentDirectory(agentId), "spool.jsonl");
}

function filenameComponent(value: string): string {
	if (
		value !== "." &&
		value !== ".." &&
		SAFE_FILENAME_COMPONENT.test(value) &&
		!value.startsWith(ENCODED_COMPONENT_PREFIX)
	) {
		return value;
	}
	return ENCODED_COMPONENT_PREFIX + Buffer.from(value, "utf8").toString("base64url");
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readStringFlag(pi: ExtensionAPI, name: string): string | undefined {
	const value = pi.getFlag(name);
	if (typeof value !== "string") return undefined;
	const trimmed = value.trim();
	return trimmed || undefined;
}

function expandHome(path: string): string {
	if (path === "~") return homedir();
	if (path.startsWith("~/")) return join(homedir(), path.slice(2));
	return path;
}

function formatReadCommand(cliPath: string, enrollment: Enrollment): string {
	return [
		shellQuote(cliPath),
		"read",
		"--workstream",
		shellQuote(enrollment.workstreamCode),
		"--agent",
		shellQuote(enrollment.agentId),
	].join(" ");
}

function shellQuote(value: string): string {
	return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function safeDisplay(value: string): string {
	return value.replace(/[\u0000-\u001f\u007f]/g, " ");
}

function errorMessage(error: unknown): string {
	return error instanceof Error ? error.message : "AirCommand adapter setup failed.";
}

function tailSpool(
	path: string,
	onNotification: (summary: string, notification: SpoolNotification) => void,
	onDiagnostic: (message: string) => void,
): TailHandle {
	mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
	chmodSync(dirname(path), 0o700);
	const descriptor = openSync(path, "a+", 0o600);
	chmodSync(path, 0o600);
	let position = fstatSync(descriptor).size;
	let pending = Buffer.alloc(0);
	let closed = false;

	const drain = () => {
		if (closed) return;
		try {
			const size = fstatSync(descriptor).size;
			if (size < position) {
				position = size;
				pending = Buffer.alloc(0);
				return;
			}
			while (position < size) {
				const chunk = Buffer.alloc(Math.min(size - position, 64 * 1024));
				const bytesRead = readSync(descriptor, chunk, 0, chunk.length, position);
				if (bytesRead === 0) break;
				position += bytesRead;
				pending = Buffer.concat([pending, chunk.subarray(0, bytesRead)]);
			}

			let newline = pending.indexOf(0x0a);
			while (newline >= 0) {
				const line = pending.subarray(0, newline).toString("utf8").trim();
				pending = pending.subarray(newline + 1);
				if (line) deliverSpoolLine(line, onNotification, onDiagnostic);
				newline = pending.indexOf(0x0a);
			}
		} catch {
			onDiagnostic("AirCommand could not read the notification spool.");
		}
	};

	let watcher: FSWatcher;
	try {
		watcher = watch(path, drain);
	} catch (error) {
		closeSync(descriptor);
		throw error;
	}
	watcher.on("error", () => {
		onDiagnostic("AirCommand notification spool watch failed.");
	});

	return {
		close() {
			if (closed) return;
			closed = true;
			watcher.close();
			closeSync(descriptor);
		},
	};
}

function deliverSpoolLine(
	line: string,
	onNotification: (summary: string, notification: SpoolNotification) => void,
	onDiagnostic: (message: string) => void,
) {
	let notification: SpoolNotification;
	try {
		notification = JSON.parse(line) as SpoolNotification;
	} catch {
		onDiagnostic("AirCommand ignored a malformed notification spool entry.");
		return;
	}
	if (typeof notification.summary !== "string" || !notification.summary) {
		onDiagnostic("AirCommand ignored a notification without a summary.");
		return;
	}
	onNotification(notification.summary, notification);
}

function notify(ctx: ExtensionContext, message: string, level: "info" | "warning" | "error") {
	if (ctx.hasUI) ctx.ui.notify(message, level);
}
