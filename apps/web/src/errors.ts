/**
 * The error the server answered with, `{"code": "artifact_not_found", "message": …}`, typed as the
 * operation declares it; undefined when there's none. The generated client rejects with that body
 * when it has one, and otherwise with whatever went wrong: a fetch that never got an answer, a body
 * that isn't JSON, a response that doesn't match its schema.
 */
export const apiError = <E extends { readonly code: string }>(error: E | null | undefined): E | undefined =>
  typeof error === "object" &&
  error !== null &&
  !(error instanceof Error) &&
  typeof (error as { readonly code?: unknown }).code === "string"
    ? error
    : undefined
