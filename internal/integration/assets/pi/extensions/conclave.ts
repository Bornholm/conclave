/**
 * Conclave - paces a Pi run to the deadline Conclave gives it, so that the run
 * answers before Conclave stops it.
 *
 * Conclave kills an agent at its timeout, and everything the model would have
 * written after the kill is lost: a review that was almost ready produces
 * nothing. This extension keeps the same deadline but makes the run converge
 * on an answer instead of being cut mid-exploration:
 *
 * - 50% of the budget: a steering message tells the model to stop exploring
 *   and start concluding. It is delivered after the current tool batch, and
 *   dropped if the model has already answered by then. Thinking is lowered to
 *   `low` for the turns that follow.
 * - 65%: every tool call is blocked with `terminate`, and thinking is lowered to
 *   `minimal`, so the model has nothing left to do but write. This is what
 *   guarantees a response: from here the only possible output is text. The
 *   remaining third is what it gets to write, which is not generosity - a model
 *   answers in one message that opens with its reasoning, and nothing in this
 *   process can cut a message in half usefully.
 * - 100%: nothing. Conclave stops the process on its own timeout.
 *
 * Together that means a stopped run still leaves an answer behind, either a
 * complete one or the truncated start of one, and Conclave reads what a killed
 * process wrote on standard output.
 *
 * It deliberately never calls `abort()`: aborting mid-answer destroys the very
 * thing the earlier stages worked to produce. Nor does it exit by itself: a
 * process that ends with status 0 at its deadline looks to Conclave like a run
 * that finished, which hides that it was stopped.
 *
 * The deadline is read from `CONCLAVE_DEADLINE_AT`, the absolute RFC 3339
 * instant Conclave exports for every agent run, and the stages from
 * `CONCLAVE_DEADLINE_MS`, the budget it counts that instant from. The instant is
 * Conclave's own, so both agree on when the run ends, whereas a countdown
 * started here would always finish after Conclave's timeout. Without the
 * instant, the budget is counted from session start.
 * Without either, outside Conclave, the extension does nothing, so it can be
 * installed for every session.
 *
 * Installed by `conclave agents install pi`.
 */

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

type ThinkingLevel = ReturnType<ExtensionAPI["getThinkingLevel"]>;

/** Exported by Conclave for every agent run with a timeout. */
const ENV_DEADLINE_AT = "CONCLAVE_DEADLINE_AT";
const ENV_BUDGET_MS = "CONCLAVE_DEADLINE_MS";

/** The value of `name`, unless it is unset or blank. */
function readEnv(name: string): string | undefined {
	const value = process.env[name];
	if (value === undefined || value.trim() === "") return undefined;
	return value;
}

/**
 * Fractions of the budget at which the run is pushed toward its answer.
 *
 * A third of the budget is kept for writing, not the last few seconds: a
 * model answers in a single message, and that message opens with its
 * reasoning. What this process cannot do is interrupt a model mid-message, so
 * the reserve has to be wide enough for one to finish. Cutting it finer than
 * this leaves the model thinking when the deadline arrives, and a message that
 * never ended counts for nothing.
 */
const CONCLUDE_AT = 0.5;
const STOP_TOOLS_AT = 0.65;

/** A budget under this is certainly a mistake, and too short to act in. */
const MIN_BUDGET_MS = 5_000;

type Stage = "work" | "conclude" | "write";

/**
 * Thinking levels from least to most, as Pi names them.
 *
 * Disabling the tools does not stop a model from spending the whole reserve
 * deliberating: a run stopped at three minutes was found with 10 to 20 thousand
 * characters of reasoning in its last turn and no answer. The level is read at
 * each request, so it is lowered as soon as the model is told to conclude, for
 * the turns that follow.
 */
const THINKING_ORDER: ThinkingLevel[] = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];

/** The thinking level each stage allows at most. */
const THINKING_AT: Partial<Record<Stage, ThinkingLevel>> = {
	conclude: "low",
	write: "minimal",
};

/** Progress is monotonic: a stage never goes back, whatever re-schedules it. */
const ORDER: Stage[] = ["work", "conclude", "write"];

function log(message: string): void {
	// stderr in every mode: stdout carries the JSON event stream and must stay
	// parseable, and Conclave keeps stderr next to the raw output.
	process.stderr.write(`[conclave] ${message}\n`);
}

/** Render a duration the way a person would say it. */
function humanize(ms: number): string {
	const s = Math.max(0, Math.round(ms / 1000));
	if (s < 90) return `${s} second${s === 1 ? "" : "s"}`;
	const m = Math.round(s / 60);
	return `${m} minute${m === 1 ? "" : "s"}`;
}

/** The system prompt section that states the contract up front. */
function promptSection(remainingMs: number, exploringMs: number, stage: Stage): string {
	const lines = [
		`You have about ${humanize(remainingMs)} of wall-clock time left before this run is stopped.`,
		"A run stopped before you write your final answer produces nothing usable:",
		"everything you had found is lost, so delivering a shorter answer in time is worth more than a complete one that never arrives.",
		"You have no tool that reads the clock; do not spend calls looking for the time, pace yourself by the work instead.",
		"- Spend the time where problems are most likely; do not dwell on what you can judge at a glance.",
		"- Read each file once, by line ranges, and only the parts you need.",
		"- Run independent reads and searches together rather than one at a time.",
		"- Once the main work is covered, stop exploring and write your answer with what you have verified.",
	];
	if (stage === "work" && exploringMs > 0) {
		// The total is not what the model can explore with: its tools go at
		// STOP_TOOLS_AT, and pacing on the total plans for work it cannot do.
		lines.splice(
			1,
			0,
			`Your tools will be disabled in about ${humanize(exploringMs)}; after that you can only write. Plan your exploration to fit before then.`,
		);
	}
	if (stage === "conclude") {
		lines.unshift(
			"You are in the last part of your budget: stop exploring now and spend the remaining time writing your final answer.",
		);
	}
	if (stage === "write") {
		lines.unshift(
			"Your tools are now disabled. The only thing left to do is to write your final answer, with what you have already verified.",
		);
	}
	return lines.join("\n");
}

/**
 * Whether an assistant message is an answer: text, and no tool call left to
 * run. A message that still calls a tool is work in progress, and a message
 * with neither is an empty step.
 */
function isAnswer(message: { role: string; content?: unknown }): boolean {
	if (message.role !== "assistant") return false;
	if (!Array.isArray(message.content)) return false;
	const blocks = message.content as { type?: string; text?: unknown }[];
	if (blocks.some((block) => block.type === "toolCall")) return false;
	return blocks.some((block) => block.type === "text" && String(block.text ?? "").trim() !== "");
}

export default function conclave(pi: ExtensionAPI) {
	// Per session, not per run: the steering message and the last-chance
	// continuation start runs of their own, and the deadline is the process's,
	// so the tools stay off and the continuation stays spent across them.
	// session_start resets it all.
	let budgetMs: number | undefined;
	/** Wall-clock instant, in epoch ms, at which the run must be over. */
	let deadlineAt = 0;
	let stage: Stage = "work";
	let sawText = false;
	let continuedOnce = false;
	/** Text waiting for a turn to end before it is delivered, if still useful. */
	let pendingSteer: string | undefined;
	let timers: ReturnType<typeof setTimeout>[] = [];

	// An absolute instant, never a countdown: `process.uptime()` starts when
	// this process did, which is after Conclave launched it, so counting
	// from there would always fire after Conclave's own timeout.
	const now = (): number => Date.now();
	const remainingMs = (): number => deadlineAt - now();

	/**
	 * Track a timer without letting it keep the process alive: a run that has
	 * answered must be free to end before its deadline.
	 */
	function track(timer: ReturnType<typeof setTimeout>): void {
		timer.unref?.();
		timers.push(timer);
	}

	function clearTimers(): void {
		for (const timer of timers) clearTimeout(timer);
		timers = [];
	}

	/** Lower the thinking level to at most the stage's, never raise it. */
	function capThinking(next: Stage): void {
		const cap = THINKING_AT[next];
		if (cap === undefined) return;
		try {
			const current = pi.getThinkingLevel();
			const rank = THINKING_ORDER.indexOf(current);
			// An unknown level is capped too: it cannot be shown to be lower.
			if (rank !== -1 && rank <= THINKING_ORDER.indexOf(cap)) {
				// A model declared without reasoning reports "off" while the model
				// behind it may still deliberate at length: say so, since nothing
				// here can bound it.
				log(`thinking already ${current}, left as is`);
				return;
			}
			pi.setThinkingLevel(cap);
			log(`thinking lowered from ${current} to ${pi.getThinkingLevel()}`);
		} catch (error) {
			log(`could not lower thinking: ${error instanceof Error ? error.message : String(error)}`);
		}
	}

	function reach(ctx: ExtensionContext, next: Stage): boolean {
		if (ORDER.indexOf(next) <= ORDER.indexOf(stage)) return false;
		stage = next;
		capThinking(next);
		const left = humanize(remainingMs());
		switch (next) {
			case "conclude":
				log(`concluding, ${left} left`);
				steer(
					ctx,
					`Time budget: about ${left} left before this run is stopped, and a run stopped early produces nothing usable. ` +
						"Stop exploring now: launch no new investigation, and spend the remaining time writing your final answer " +
						"with what you have already verified. Prefer a shorter answer that arrives to a complete one that does not.",
				);
				break;
			case "write":
				// No steering message here: the blocked calls tell the model, in the
				// same turn, that it has nothing left to do but answer. A second
				// steer would queue a second turn and a second answer, spending the
				// reserve on work that is already done.
				log(`tools disabled, ${left} left`);
				break;
			default:
				break;
		}
		return true;
	}

	function steer(ctx: ExtensionContext, text: string): void {
		// Held until a turn ends: a message queued while the model is still
		// working arrives after that turn's batch of tools, and there is no gain
		// in interrupting a model that is already writing its answer.
		pendingSteer = text;
		flushSteer(ctx);
	}

	/**
	 * Deliver the held message, unless the last turn no longer needs it. It is
	 * only sent while the run streams, so that it queues as a steering message
	 * rather than starting a turn of its own.
	 */
	function flushSteer(ctx: ExtensionContext): void {
		const text = pendingSteer;
		if (text === undefined) return;
		if (sawText) {
			// The model already wrote an answer: redirecting it now can only
			// produce a second one, and spend the reserve twice.
			pendingSteer = undefined;
			log("steer dropped: the run already produced an answer");
			return;
		}
		pendingSteer = undefined;
		if (ctx.isIdle()) {
			// The run it was meant for is over: held, it would land in whatever
			// run comes next, with a budget that is not this one's.
			log("steer dropped: no run in progress");
			return;
		}
		try {
			pi.sendUserMessage(text, { deliverAs: "steer" });
		} catch (error) {
			log(`could not steer: ${error instanceof Error ? error.message : String(error)}`);
		}
	}

	/**
	 * (Re)schedule the stages from the current time. Called on every run so the
	 * timers hold a live context: a session replacement invalidates the old one.
	 */
	function schedule(ctx: ExtensionContext): void {
		clearTimers();
		if (!budgetMs || !deadlineAt) return;
		const marks: [Stage, number][] = [
			["conclude", budgetMs * CONCLUDE_AT],
			["write", budgetMs * STOP_TOOLS_AT],
		];
		for (const [next, at] of marks) {
			if (ORDER.indexOf(next) <= ORDER.indexOf(stage)) continue;
			const delay = deadlineAt - budgetMs + at - now();
			if (delay <= 0) {
				reach(ctx, next);
				continue;
			}
			track(
				setTimeout(() => {
					// Outside any handler: a throw here, from a context a session
					// replacement invalidated for instance, would end the process
					// and lose the very answer this stage is meant to secure.
					try {
						reach(ctx, next);
						// Later stages still need their own timer.
						schedule(ctx);
					} catch (error) {
						log(`stage ${next} failed: ${error instanceof Error ? error.message : String(error)}`);
					}
				}, delay),
			);
		}
	}

	pi.on("session_start", (_event, _ctx) => {
		// A replaced session starts over, and one without a deadline leaves the
		// extension inert rather than holding the previous session's.
		clearTimers();
		budgetMs = undefined;
		deadlineAt = 0;
		stage = "work";
		sawText = false;
		continuedOnce = false;
		pendingSteer = undefined;

		const envAt = readEnv(ENV_DEADLINE_AT);
		const envBudget = readEnv(ENV_BUDGET_MS);

		// The absolute instant is preferred: a duration counted from here would
		// start after Conclave launched this process, and land after Conclave's
		// own timeout. It is Conclave's instant, so both agree.
		let at = 0;
		let ms: number | undefined;
		if (envAt !== undefined) {
			const parsed = Date.parse(envAt);
			if (Number.isFinite(parsed)) at = parsed;
			else log(`ignoring ${ENV_DEADLINE_AT}=${JSON.stringify(envAt)}: expected an RFC 3339 instant`);
		}
		// Read even with the instant: the stages are fractions of the budget
		// Conclave gave, counted from when it launched this process, not of
		// whatever is left once Pi has started.
		if (envBudget !== undefined) {
			const parsed = Number(envBudget);
			if (Number.isFinite(parsed) && parsed > 0) ms = parsed;
			else log(`ignoring ${ENV_BUDGET_MS}=${JSON.stringify(envBudget)}: expected milliseconds`);
		}
		if (at === 0 && ms !== undefined) at = now() + ms;
		if (at === 0) return;

		const left = at - now();
		if (left <= 0) {
			log("the deadline has already passed, nothing to schedule");
			return;
		}
		if (left < MIN_BUDGET_MS) {
			log(`ignoring ${humanize(left)} left: under ${MIN_BUDGET_MS}ms there is no room to converge`);
			return;
		}
		deadlineAt = at;
		// A budget longer than the time to the instant would anchor the stages
		// before the process could have been launched.
		budgetMs = ms !== undefined && ms >= left ? ms : left;
		log(
			`budget ${humanize(budgetMs)}, concluding at ${humanize(budgetMs * CONCLUDE_AT)}, tools off at ${humanize(budgetMs * STOP_TOOLS_AT)}`,
		);
	});

	pi.on("agent_start", (_event, ctx) => {
		if (!budgetMs) return;
		// Each run holds a fresh context; re-anchor the timers to it.
		schedule(ctx);
	});

	pi.on("before_agent_start", (event) => {
		if (!budgetMs) return;
		const toolsOffAt = deadlineAt - budgetMs + budgetMs * STOP_TOOLS_AT;
		event.systemPromptOptions.sections.deadline = promptSection(remainingMs(), toolsOffAt - now(), stage);
	});

	pi.on("tool_call", (event) => {
		if (!budgetMs) return;
		if (stage !== "write") return;
		return {
			block: true,
			terminate: true,
			reason:
				`The time budget for this run is nearly over: tool ${event.toolName} is disabled. ` +
				"Write your final answer now with what you have already verified.",
		};
	});

	pi.on("message_end", (event) => {
		if (!budgetMs) return;
		if (event.message.role !== "assistant") return;
		if (isAnswer(event.message)) sawText = true;
	});

	pi.on("turn_end", (event, ctx) => {
		if (!budgetMs) return;
		// `turn_end` is dispatched before `message_end`, so the answer has to be
		// read from the event itself: waiting for `message_end` would deliver a
		// held message to a run that had just finished answering.
		if (isAnswer(event.message)) sawText = true;
		flushSteer(ctx);
	});

	// Last chance for a run that reached its deadline without writing anything:
	// one continuation, and only one, so a model that keeps calling tools
	// cannot loop here.
	pi.on("agent_before_settle", (event) => {
		if (!budgetMs) return;
		// Only once the tools are off: the message below says so, and an earlier
		// run that settled without text would spend the one continuation on it.
		if (sawText || continuedOnce) return;
		if (event.outcome === "aborted" || event.outcome === "error") return;
		if (stage !== "write") {
			// Said, so that an empty run is not a mystery in the logs.
			log("run settled without text before its tools were cut, not continued");
			return;
		}
		continuedOnce = true;
		log("run settled without text, asking once more");
		return {
			continue: true,
			entries: [
				{
					type: "custom_message",
					customType: "conclave-deadline",
					display: false,
					content:
						"The time budget for this run is exhausted and you have not written your final answer yet. " +
						"Your tools are disabled. Write your final answer now, with everything you have already verified, " +
						"and state plainly what you could not check.",
				},
			],
		};
	});

	pi.on("session_shutdown", () => {
		clearTimers();
	});
}
