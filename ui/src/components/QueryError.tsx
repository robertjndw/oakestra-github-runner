import { QueryErrorAlert } from '@oakestra/ui';

import { errorMessage } from '../api/client';

export function QueryError({
  title,
  error,
  onRetry,
}: {
  title: string;
  error: unknown;
  onRetry: () => void;
}) {
  // QueryErrorAlert reads error.message, so hand it the readable text in an Error.
  return <QueryErrorAlert title={title} error={new Error(errorMessage(error))} onRetry={onRetry} />;
}
