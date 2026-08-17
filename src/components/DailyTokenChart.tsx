import Column from '@ant-design/plots/es/components/column';
import { formatNumber, formatUsd } from '../format';
import type { DailyUsageItem } from '../types';

export default function DailyTokenChart({ items }: { items: DailyUsageItem[] }) {
  const data = items.slice(-14).map((item) => ({
    ...item,
    dateLabel: item.date.slice(5).replace('-', '/')
  }));

  return (
    <Column
      data={data}
      xField="dateLabel"
      yField="totalTokens"
      height={165}
      axis={{
        y: false,
        x: { title: false }
      }}
      tooltip={{
        title: 'date',
        items: [
          {
            field: 'totalTokens',
            name: 'Token',
            valueFormatter: (value: unknown) => formatNumber(Number(value))
          },
          {
            field: 'estimatedCostUsd',
            name: '费用',
            valueFormatter: (value: unknown) => formatUsd(Number(value))
          }
        ]
      }}
      style={{
        fill: '#0d9488',
        radiusTopLeft: 2,
        radiusTopRight: 2
      }}
    />
  );
}
