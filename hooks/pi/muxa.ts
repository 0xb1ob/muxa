/**
 * Oh My Pi / pi-coding-agent extension (project or user).
 * Presence via tmux pane options; drain via session_stop native continue.
 */
export default function muxa(pi: {
  exec: (command: string, args: string[]) => Promise<{ stdout?: string; code?: number }>
  on: (event: string, handler: (...args: any[]) => any) => void
  registerCommand: (name: string, spec: { description: string; handler: Function }) => void
}) {
  const bin = process.env.MUXA_BIN || "muxa"

  async function run(args: string[]) {
    try {
      return await pi.exec(bin, args)
    } catch {
      return { stdout: "", code: 1 }
    }
  }

  pi.on("session_start", async () => {
    await run(["hook", "session-start", "--kind", "pi"])
  })

  pi.on("agent_start", async () => {
    await run(["hook", "busy"])
  })

  pi.on("session_stop", async () => {
    const result = await run(["hook", "stop", "--format", "pi"])
    const text = (result.stdout || "").trim()
    if (!text) return
    try {
      return JSON.parse(text)
    } catch {
      return { continue: true, additionalContext: text }
    }
  })

  pi.registerCommand("muxa-who", {
    description: "List muxa agents in this tmux server",
    handler: async (_args: string, ctx: { ui: { notify: (msg: string, level: string) => void } }) => {
      const result = await run(["who"])
      ctx.ui.notify(result.stdout || "muxa who failed", "info")
    },
  })
}
