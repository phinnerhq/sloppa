import { statSync } from "node:fs"
import { win32 } from "node:path"

/**
 * How to spawn a command so it runs the way a terminal would, after T3 Code's `resolveSpawnCommand`
 * (packages/shared/src/shell.ts).
 *
 * On macOS and Linux that's the command itself. On Windows, `pnpm`, `npm`, `yarn`, `vite` and
 * most other dev tools are `.cmd` shims, and Node refuses to spawn those without a shell
 * (EINVAL, since CVE-2024-27980). So the command is looked up on PATH like cmd.exe would, and a
 * `.cmd` or `.bat` runs through the shell as one escaped command line. Not as `shell: true` with
 * separate args, which Node deprecates (DEP0190) because it doesn't escape them.
 */
export interface SpawnCommand {
  readonly command: string
  readonly args: ReadonlyArray<string>
  readonly shell: boolean
}

/** cmd.exe's metacharacters, caret-escaped outside and inside quotes alike. */
const WINDOWS_SHELL_META_CHARS = /([()\][%!^"`<>&|;, *?])/g

/** One argument for a cmd.exe command line, read back as itself by the program it starts. */
export const escapeWindowsShellArg = (arg: string) => {
  let escaped = arg.replace(/(\\*)"/g, '$1$1\\"')
  // Double up trailing backslashes so the closing quote is not escaped away.
  escaped = escaped.replace(/(\\*)$/, "$1$1")
  return `"${escaped}"`.replace(WINDOWS_SHELL_META_CHARS, "^$1")
}

const isFile = (path: string) => {
  try {
    return statSync(path).isFile()
  } catch {
    return false
  }
}

const pathExtensions = (env: NodeJS.ProcessEnv) => {
  const listed = (env["PATHEXT"] ?? "")
    .split(";")
    .map((entry) => entry.trim().toUpperCase())
    .filter((entry) => entry !== "")
    .map((entry) => (entry.startsWith(".") ? entry : `.${entry}`))
  return listed.length > 0 ? [...new Set(listed)] : [".COM", ".EXE", ".BAT", ".CMD"]
}

/** Where cmd.exe would find `command`: as given if it has a known extension, else with each of PATHEXT's. */
const resolveWindowsExecutable = (
  command: string,
  env: NodeJS.ProcessEnv,
  exists: (path: string) => boolean = isFile
) => {
  const extensions = pathExtensions(env)
  const extension = win32.extname(command).toUpperCase()
  // Both cases, for the case-sensitive folders Windows can have too.
  const candidates = extensions.includes(extension)
    ? [command]
    : extensions.flatMap((ext) => [`${command}${ext}`, `${command}${ext.toLowerCase()}`])
  if (command.includes("/") || command.includes("\\")) return candidates.find(exists)
  const path = env["PATH"] ?? env["Path"] ?? env["path"] ?? ""
  for (const entry of path.split(";")) {
    const dir = entry.trim().replace(/^"(.*)"$/, "$1")
    if (dir === "") continue
    const found = candidates.map((candidate) => win32.join(dir, candidate)).find(exists)
    if (found !== undefined) return found
  }
  return undefined
}

export const resolveSpawnCommand = (
  command: string,
  args: ReadonlyArray<string>,
  options: {
    readonly platform?: NodeJS.Platform
    readonly env?: NodeJS.ProcessEnv
    readonly exists?: (path: string) => boolean
  } = {}
): SpawnCommand => {
  const { platform = process.platform, env = process.env, exists = isFile } = options
  if (platform !== "win32") return { command, args, shell: false }
  const resolved = resolveWindowsExecutable(command, env, exists) ?? command
  const extension = win32.extname(resolved).toLowerCase()
  if (extension !== ".cmd" && extension !== ".bat") return { command: resolved, args, shell: false }
  // Node runs a shell spawn as `cmd.exe /d /s /c "<line>"`: build the escaped line, pass no args.
  return { command: [resolved, ...args].map(escapeWindowsShellArg).join(" "), args: [], shell: true }
}
