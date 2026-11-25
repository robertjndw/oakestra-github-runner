import { PageHeader } from '@oakestra/ui';

import { ReadOnlyNotice, useCanWrite } from '../components/permissions';
import { AppWizard } from '../settings/AppWizard';
import { ConnectionsSection } from '../settings/ConnectionsSection';
import { OakestraSection } from '../settings/OakestraSection';
import { TokenForm } from '../settings/TokenForm';

export default function SettingsPage() {
  const canWrite = useCanWrite();
  return (
    <div className="ghr:space-y-6">
      <PageHeader title="Settings" description="Connect GitHub and the Oakestra service user." />
      {canWrite ? null : <ReadOnlyNotice what="the settings" />}
      <OakestraSection />
      <ConnectionsSection />
      <AppWizard />
      <TokenForm />
    </div>
  );
}
