import { useAddonPath } from '@oakestra/dashboard-sdk';
import { StatCard } from '@oakestra/ui';
import { ContainerIcon } from 'lucide-react';
import { Link } from 'react-router';

import { useStatus } from '../api/hooks';

/** Stat tile for the overview. The shell puts all stat widgets in one strip, so no frame here. */
export function BusyRunnersWidget() {
  const status = useStatus();
  const addonPath = useAddonPath();
  const runners = status.data?.runners;

  return (
    <StatCard
      asChild
      label="Busy runners"
      icon={<ContainerIcon aria-hidden />}
      value={status.isError ? '-' : (runners?.busy ?? 0)}
      loading={status.isPending}
      hint={
        status.isError
          ? 'Runner backend unavailable'
          : `${runners?.idle ?? 0} idle, ${runners?.pending ?? 0} pending`
      }
    >
      <Link to={addonPath('runners')} />
    </StatCard>
  );
}
