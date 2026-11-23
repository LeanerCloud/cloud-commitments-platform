/**
 * Tests for commitmentOptions module
 */
import {
  getCommitmentConfig,
  isValidCombination,
  getValidPaymentOptions,
  getValidTermOptions,
} from '../commitmentOptions';

describe('commitmentOptions', () => {
  describe('getCommitmentConfig', () => {
    describe('AWS provider', () => {
      it('should return EC2 config with all payment options', () => {
        const config = getCommitmentConfig('aws', 'ec2');

        expect(config.terms).toHaveLength(2);
        expect(config.terms[0]).toEqual({ value: 1, label: '1 Year' });
        expect(config.terms[1]).toEqual({ value: 3, label: '3 Years' });

        expect(config.payments).toHaveLength(3);
        expect(config.payments.map(p => p.value)).toEqual([
          'no-upfront',
          'partial-upfront',
          'all-upfront'
        ]);

        expect(config.invalidCombinations).toBeUndefined();
      });

      it('resolves every Savings Plans slug through aws._default (#7)', () => {
        const fallback = getCommitmentConfig('aws', 'no-such-service');
        for (const slug of [
          'savingsplans', 'savings-plans-compute', 'savings-plans-ec2instance',
          'savings-plans-sagemaker', 'savings-plans-database'
        ]) {
          expect(getCommitmentConfig('aws', slug)).toBe(fallback);
        }
        expect(fallback.terms.map(t => t.value)).toEqual([1, 3]);
        expect(fallback.payments.map(p => p.value)).toEqual(['no-upfront', 'partial-upfront', 'all-upfront']);
        expect(fallback.invalidCombinations).toBeUndefined();
      });

      it('should return savingsplans config with all options', () => {
        const config = getCommitmentConfig('aws', 'savingsplans');

        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
        expect(config.invalidCombinations).toBeUndefined();
      });

      it('should return RDS config with invalid 3yr no-upfront combination', () => {
        const config = getCommitmentConfig('aws', 'rds');

        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
        expect(config.invalidCombinations).toBeDefined();
        expect(config.invalidCombinations).toHaveLength(1);
        expect(config.invalidCombinations![0]).toEqual({ term: 3, payment: 'no-upfront' });
      });

      it.each(['elasticache', 'opensearch', 'redshift', 'memorydb'])(
        'should return %s config with no invalidCombinations (AWS restricts only RDS 3yr no-upfront)',
        (service) => {
          const config = getCommitmentConfig('aws', service);
          // These services were previously listed as also rejecting 3yr
          // no-upfront, but that was over-cautious — AWS does offer it.
          // The backend agrees: cmd/validators.go:warnRDS3YearNoUpfront
          // warns only on RDS. They fall through to the AWS _default.
          expect(config.invalidCombinations).toBeUndefined();
        },
      );

      it('should return default AWS config for unknown service', () => {
        const config = getCommitmentConfig('aws', 'unknown-service');

        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
        expect(config.invalidCombinations).toBeUndefined();
      });

      it('should return default AWS config when no service specified', () => {
        const config = getCommitmentConfig('aws');

        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
        expect(config.invalidCombinations).toBeUndefined();
      });

      it('should handle case-insensitive provider names', () => {
        const config1 = getCommitmentConfig('AWS', 'ec2');
        const config2 = getCommitmentConfig('aws', 'ec2');
        const config3 = getCommitmentConfig('Aws', 'EC2');

        expect(config1).toEqual(config2);
        // Note: service is also lowercased
        expect(config2).toEqual(config3);
      });

      it('should handle case-insensitive service names', () => {
        const config1 = getCommitmentConfig('aws', 'RDS');
        const config2 = getCommitmentConfig('aws', 'rds');

        expect(config1).toEqual(config2);
      });
    });

    describe('Azure provider', () => {
      it('should return Azure default config with upfront and monthly payments', () => {
        const config = getCommitmentConfig('azure');

        expect(config.terms).toHaveLength(2);
        expect(config.terms[0]).toEqual({ value: 1, label: '1 Year' });
        expect(config.terms[1]).toEqual({ value: 3, label: '3 Years' });

        expect(config.payments).toHaveLength(2);
        expect(config.payments[0]).toEqual({ value: 'upfront', label: 'Pay Upfront' });
        expect(config.payments[1]).toEqual({ value: 'monthly', label: 'Pay Monthly' });

        expect(config.invalidCombinations).toBeUndefined();
      });

      it('should return same config for any Azure service', () => {
        const config1 = getCommitmentConfig('azure', 'vm');
        const config2 = getCommitmentConfig('azure', 'sql');
        const config3 = getCommitmentConfig('azure');

        expect(config1).toEqual(config2);
        expect(config2).toEqual(config3);
      });
    });

    describe('GCP provider', () => {
      it('should return GCP config with only monthly payment option', () => {
        const config = getCommitmentConfig('gcp');

        expect(config.terms).toHaveLength(2);
        expect(config.terms[0]).toEqual({ value: 1, label: '1 Year' });
        expect(config.terms[1]).toEqual({ value: 3, label: '3 Years' });

        expect(config.payments).toHaveLength(1);
        expect(config.payments[0]).toEqual({ value: 'monthly', label: 'Monthly' });

        expect(config.invalidCombinations).toBeUndefined();
      });

      it('should return same config for any GCP service', () => {
        const config1 = getCommitmentConfig('gcp', 'compute');
        const config2 = getCommitmentConfig('gcp', 'cloudsql');
        const config3 = getCommitmentConfig('gcp');

        expect(config1).toEqual(config2);
        expect(config2).toEqual(config3);
      });
    });

    describe('Unknown provider', () => {
      it('should return default config for unknown provider', () => {
        const config = getCommitmentConfig('unknown-provider');

        // Default config uses AWS payments
        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
        expect(config.payments.map(p => p.value)).toEqual([
          'no-upfront',
          'partial-upfront',
          'all-upfront'
        ]);
      });

      it('should return default config for empty provider string', () => {
        const config = getCommitmentConfig('');

        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
      });

      // #320: plain record[key] lookups resolved prototype members, so
      // getCommitmentConfig('constructor', 'name') returned "Object".
      it.each([
        ['constructor', 'name'],
        ['__proto__', 'constructor'],
        ['aws', 'constructor'],
        ['aws', '__proto__'],
      ])('ignores prototype members as provider/service (%s, %s)', (provider, service) => {
        const config = getCommitmentConfig(provider, service);

        expect(config.terms).toHaveLength(2);
        expect(config.payments).toHaveLength(3);
      });
    });
  });

  describe('isValidCombination', () => {
    describe('AWS services without restrictions', () => {
      it('should return true for all EC2 combinations', () => {
        expect(isValidCombination('aws', 'ec2', 1, 'no-upfront')).toBe(true);
        expect(isValidCombination('aws', 'ec2', 1, 'partial-upfront')).toBe(true);
        expect(isValidCombination('aws', 'ec2', 1, 'all-upfront')).toBe(true);
        expect(isValidCombination('aws', 'ec2', 3, 'no-upfront')).toBe(true);
        expect(isValidCombination('aws', 'ec2', 3, 'partial-upfront')).toBe(true);
        expect(isValidCombination('aws', 'ec2', 3, 'all-upfront')).toBe(true);
      });

      it('should return true for all savingsplans combinations', () => {
        expect(isValidCombination('aws', 'savingsplans', 1, 'no-upfront')).toBe(true);
        expect(isValidCombination('aws', 'savingsplans', 3, 'no-upfront')).toBe(true);
      });

      // Issue #22 follow-up: per-plan-type SP slugs fall through the
      // _default arm of getCommitmentConfig (no per-key entry needed),
      // so all 6 (term × payment) combos are valid for each. This test
      // pins that behaviour so a future change to the _default
      // fallback can't silently restrict SP saves.
      it.each([
        'savings-plans-compute',
        'savings-plans-ec2instance',
        'savings-plans-sagemaker',
        'savings-plans-database',
      ])('should return true for all %s combinations (24 total: 4 keys × 6 combos)', (service) => {
        for (const term of [1, 3]) {
          for (const payment of ['no-upfront', 'partial-upfront', 'all-upfront']) {
            expect(isValidCombination('aws', service, term, payment)).toBe(true);
          }
        }
      });
    });

    describe('AWS services with 3yr no-upfront restriction', () => {
      // Only RDS has this restriction. ElastiCache / OpenSearch /
      // Redshift / MemoryDB were previously listed too but AWS does
      // offer 3yr no-upfront for those.
      it('should return false for rds 3yr no-upfront', () => {
        expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(false);
      });

      it('should return true for rds 1yr no-upfront', () => {
        expect(isValidCombination('aws', 'rds', 1, 'no-upfront')).toBe(true);
      });

      it('should return true for rds 3yr partial-upfront', () => {
        expect(isValidCombination('aws', 'rds', 3, 'partial-upfront')).toBe(true);
      });

      it('should return true for rds 3yr all-upfront', () => {
        expect(isValidCombination('aws', 'rds', 3, 'all-upfront')).toBe(true);
      });

      it.each(['elasticache', 'opensearch', 'redshift', 'memorydb'])(
        'should return true for %s 3yr no-upfront (not restricted)',
        (service) => {
          expect(isValidCombination('aws', service, 3, 'no-upfront')).toBe(true);
        },
      );
    });

    describe('Azure and GCP', () => {
      it('should return true for all Azure combinations', () => {
        expect(isValidCombination('azure', 'vm', 1, 'upfront')).toBe(true);
        expect(isValidCombination('azure', 'vm', 3, 'upfront')).toBe(true);
        expect(isValidCombination('azure', 'vm', 1, 'monthly')).toBe(true);
        expect(isValidCombination('azure', 'vm', 3, 'monthly')).toBe(true);
      });

      it('should return true for all GCP combinations', () => {
        expect(isValidCombination('gcp', 'compute', 1, 'monthly')).toBe(true);
        expect(isValidCombination('gcp', 'compute', 3, 'monthly')).toBe(true);
      });
    });

    describe('Edge cases', () => {
      it('should handle undefined service', () => {
        expect(isValidCombination('aws', undefined, 1, 'no-upfront')).toBe(true);
        expect(isValidCombination('aws', undefined, 3, 'no-upfront')).toBe(true);
      });

      it('should handle unknown provider', () => {
        expect(isValidCombination('unknown', 'service', 1, 'no-upfront')).toBe(true);
      });
    });
  });

  describe('getValidPaymentOptions', () => {
    describe('AWS services without restrictions', () => {
      it('should return all payment options for EC2 1-year term', () => {
        const options = getValidPaymentOptions('aws', 'ec2', 1);

        expect(options).toHaveLength(3);
        expect(options.map(o => o.value)).toEqual([
          'no-upfront',
          'partial-upfront',
          'all-upfront'
        ]);
      });

      it('should return all payment options for EC2 3-year term', () => {
        const options = getValidPaymentOptions('aws', 'ec2', 3);

        expect(options).toHaveLength(3);
      });
    });

    describe('AWS services with 3yr no-upfront restriction', () => {
      it('should exclude no-upfront for RDS 3-year term', () => {
        const options = getValidPaymentOptions('aws', 'rds', 3);

        expect(options).toHaveLength(2);
        expect(options.map(o => o.value)).toEqual([
          'partial-upfront',
          'all-upfront'
        ]);
      });

      it('should return all options for RDS 1-year term', () => {
        const options = getValidPaymentOptions('aws', 'rds', 1);

        expect(options).toHaveLength(3);
        expect(options.map(o => o.value)).toContain('no-upfront');
      });

      it.each(['elasticache', 'opensearch', 'redshift', 'memorydb'])(
        'should keep no-upfront for %s 3-year term (AWS offers it)',
        (service) => {
          const options = getValidPaymentOptions('aws', service, 3);

          expect(options).toHaveLength(3);
          expect(options.map(o => o.value)).toContain('no-upfront');
        },
      );
    });

    describe('Azure', () => {
      it('should return Azure payment options', () => {
        const options = getValidPaymentOptions('azure', 'vm', 1);

        expect(options).toHaveLength(2);
        expect(options.map(o => o.value)).toEqual(['upfront', 'monthly']);
      });
    });

    describe('GCP', () => {
      it('should return only monthly option', () => {
        const options = getValidPaymentOptions('gcp', 'compute', 1);

        expect(options).toHaveLength(1);
        expect(options[0]!.value).toBe('monthly');
      });
    });

    describe('Edge cases', () => {
      it('should handle undefined service', () => {
        const options = getValidPaymentOptions('aws', undefined, 1);

        expect(options).toHaveLength(3);
      });
    });
  });

  describe('getValidTermOptions', () => {
    describe('AWS services without restrictions', () => {
      it('should return all term options for EC2 with no-upfront', () => {
        const options = getValidTermOptions('aws', 'ec2', 'no-upfront');

        expect(options).toHaveLength(2);
        expect(options.map(o => o.value)).toEqual([1, 3]);
      });

      it('should return all term options for EC2 with all-upfront', () => {
        const options = getValidTermOptions('aws', 'ec2', 'all-upfront');

        expect(options).toHaveLength(2);
      });
    });

    describe('AWS services with 3yr no-upfront restriction', () => {
      it('should exclude 3-year for RDS with no-upfront', () => {
        const options = getValidTermOptions('aws', 'rds', 'no-upfront');

        expect(options).toHaveLength(1);
        expect(options[0]).toEqual({ value: 1, label: '1 Year' });
      });

      it('should return all terms for RDS with partial-upfront', () => {
        const options = getValidTermOptions('aws', 'rds', 'partial-upfront');

        expect(options).toHaveLength(2);
        expect(options.map(o => o.value)).toEqual([1, 3]);
      });

      it('should return all terms for RDS with all-upfront', () => {
        const options = getValidTermOptions('aws', 'rds', 'all-upfront');

        expect(options).toHaveLength(2);
      });

      it.each(['elasticache', 'opensearch', 'redshift', 'memorydb'])(
        'should keep 3-year for %s with no-upfront (AWS offers it)',
        (service) => {
          const options = getValidTermOptions('aws', service, 'no-upfront');

          expect(options).toHaveLength(2);
          expect(options.map(o => o.value)).toEqual([1, 3]);
        },
      );
    });

    describe('Azure', () => {
      it('should return all term options for any payment', () => {
        const upfrontOptions = getValidTermOptions('azure', 'vm', 'upfront');
        const monthlyOptions = getValidTermOptions('azure', 'vm', 'monthly');

        expect(upfrontOptions).toHaveLength(2);
        expect(monthlyOptions).toHaveLength(2);
      });
    });

    describe('GCP', () => {
      it('should return all term options for monthly payment', () => {
        const options = getValidTermOptions('gcp', 'compute', 'monthly');

        expect(options).toHaveLength(2);
        expect(options.map(o => o.value)).toEqual([1, 3]);
      });
    });

    describe('Edge cases', () => {
      it('should handle undefined service', () => {
        const options = getValidTermOptions('aws', undefined, 'no-upfront');

        expect(options).toHaveLength(2);
      });
    });
  });

});
