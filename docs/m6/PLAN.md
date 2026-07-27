# M6 实施计划 — bet-svc(Closed Pool + 结算)

> 配套 [ADR-0006](../adr/0006-bet-pools.md)。验收:`make up && make smoke-m6`。

## 验收口径(smoke_m6.sh)

1. 两用户注册+登录(M2),钱包自动开(M3 consumer),各充值。
2. A 建房,B 加入(M4);A 在房间建 Pool(YES/NO)。
3. A 押 YES 1000、B 押 NO 600(wallet 扣款);**同幂等键重放不双扣**。
4. 非成员押注 → 403;余额不足 → 422。
5. A 结算(YES 胜):A 收回 1000 + floor(600×95%) = 1570;B 不动;余额精确断言(rake 5% 归庄)。
6. 重复 Settle → 幂等(余额不变);Kafka `aurora.bet.lifecycle.v1` 出现 bet.placed / pool.settled 事件。

## 交付物

proto `aurora/bet/v1/bet.proto`;avsc `bet.pool.settled.v1.avsc`;
`services/bet-svc/`(config / auth 拷贝 / store 含结算数学 / walletclient / roomclient / events+kafka / server / main / Dockerfile / go.mod / 单测含 parimutuel 数学表驱动);compose(8086)+ Makefile + smoke。

## 不在 M6 范围

赛果 oracle / 自动结算(M9/M13)、多于两选项、撤单、bet outbox(债务已登记)、真钱(M9)。
