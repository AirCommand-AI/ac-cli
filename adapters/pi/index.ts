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

const WORKSTREAM_FLAG = "aircommand-workstream";
const AGENT_FLAG = "aircommand-agent";
const CLI_FLAG = "aircommand-cli";

interface Enrollment {
	workstreamCode: string;
	agentId: string;
}

interface StoredCredential {
	workstreamCode?: unknown;
	agentId?: unknown;
}

interface CredentialFile {
	agents?: Record<string, StoredCredential>;
}

interface SpoolNotification {
	type?: unknown;
	updateId?: unknown;
	summary?: unknown;
}

interface TailHandle {
	close(): void;
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

	let tail: TailHandle | undefined;
	let active = false;

	pi.on("session_start", async (_event, ctx) => {
		active = true;
		tail?.close();
		tail = undefined;

		try {
			const enrollment = resolveEnrollment(pi);
			const cliPath = expandHome(readStringFlag(pi, CLI_FLAG) ?? join(homedir(), ".local", "bin", "ac-cli"));
			const spoolPath = join(homedir(), ".aircommand", "spool", `${enrollment.workstreamCode}.jsonl`);
			const readCommand = formatReadCommand(cliPath, enrollment);

			tail = tailSpool(
				spoolPath,
				(summary, notification) => {
					if (!active) return;
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
				(message) => notify(ctx, message, "warning"),
			);

			notify(
				ctx,
				`AirCommand is watching workstream ${enrollment.workstreamCode} for agent ${enrollment.agentId}.`,
				"info",
			);
		} catch (error) {
			const message = error instanceof Error ? error.message : "AirCommand adapter setup failed.";
			notify(ctx, message, "error");
		}
	});

	pi.on("session_shutdown", async () => {
		active = false;
		tail?.close();
		tail = undefined;
	});
}

function resolveEnrollment(pi: ExtensionAPI): Enrollment {
	const requestedWorkstream = readStringFlag(pi, WORKSTREAM_FLAG);
	const requestedAgent = readStringFlag(pi, AGENT_FLAG);
	if (requestedWorkstream && requestedAgent) {
		return validateEnrollment({ workstreamCode: requestedWorkstream, agentId: requestedAgent });
	}

	const credentialsPath = join(homedir(), ".aircommand", "credentials.json");
	let parsed: CredentialFile;
	try {
		parsed = JSON.parse(readFileSync(credentialsPath, "utf8")) as CredentialFile;
	} catch {
		throw new Error(
			`AirCommand enrollment metadata is unavailable. Pass --${WORKSTREAM_FLAG} and --${AGENT_FLAG}.`,
		);
	}

	const enrollments: Enrollment[] = [];
	for (const [key, value] of Object.entries(parsed.agents ?? {})) {
		if (typeof value?.workstreamCode !== "string") continue;
		const agentId = typeof value.agentId === "string" ? value.agentId : key;
		if (!agentId) continue;
		enrollments.push({ workstreamCode: value.workstreamCode, agentId });
	}

	const matches = enrollments.filter((enrollment) => {
		if (requestedWorkstream && enrollment.workstreamCode !== requestedWorkstream) return false;
		if (requestedAgent && enrollment.agentId !== requestedAgent) return false;
		return true;
	});
	if (matches.length !== 1) {
		throw new Error(
			`AirCommand enrollment is ambiguous or missing. Pass --${WORKSTREAM_FLAG} and --${AGENT_FLAG}.`,
		);
	}
	return validateEnrollment(matches[0]);
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

function tailSpool(
	path: string,
	onNotification: (summary: string, notification: SpoolNotification) => void,
	onDiagnostic: (message: string) => void,
): TailHandle {
	mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
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

	const watcher: FSWatcher = watch(path, drain);
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
