import { usePermissions } from '@oakestra/dashboard-sdk';
import { Alert, AlertDescription } from '@oakestra/ui';
import { LockIcon } from 'lucide-react';

/** Writes need the Admin role. The backend enforces it too, this only spares users dead ends. */
export function useCanWrite(): boolean {
  return usePermissions().hasRole('Admin');
}

export const READ_ONLY_REASON = 'Only administrators can change this.';

export function ReadOnlyNotice({ what }: { what: string }) {
  return (
    <Alert variant="info">
      <LockIcon aria-hidden />
      <AlertDescription>
        You can view {what}, but only administrators can change it.
      </AlertDescription>
    </Alert>
  );
}
