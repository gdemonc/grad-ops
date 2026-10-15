# 测试报告

## 测试统计

执行环境：Windows / Python 3.11.2 / pytest 9.1.1 / pytest-cov 7.1.0，工作目录为项目根目录。

执行命令：

```powershell
python -m pytest --cov=app --cov-report=term
```

说明：`pyproject.toml` 的 `addopts` 已包含 `-q`；若再叠加一个 `-q`（即 `python -m pytest -q --cov=app --cov-report=term`）会变成 `-qq`，pytest 将省略末尾的 `137 passed, 1 skipped` 汇总行，测试与覆盖数据完全相同。

终端输出（原样粘贴）：

```text
..........................................s............................. [ 52%]
..................................................................       [100%]
=============================== tests coverage ================================
_______________ coverage: platform win32, python 3.11.2-final-0 _______________

Name                       Stmts   Miss  Cover
----------------------------------------------
app\__init__.py                0      0   100%
app\api\__init__.py            0      0   100%
app\api\approvals.py          87      4    95%
app\api\audit.py              48      7    85%
app\api\tasks.py              47      0   100%
app\audit.py                  40      0   100%
app\config.py                 16      0   100%
app\contract.py               20      1    95%
app\db.py                     17      4    76%
app\enums.py                  33      0   100%
app\executor.py              110      8    93%
app\grounding.py              49      0   100%
app\investigation.py         125      6    95%
app\llm\__init__.py            2      0   100%
app\llm\client.py             70      3    96%
app\main.py                   15      0   100%
app\models.py                 98      0   100%
app\platform.py               29      5    83%
app\policy\__init__.py         2      0   100%
app\policy\boundary.py        20      1    95%
app\policy\controller.py      58      1    98%
app\policy\rules.py           10      0   100%
app\queue.py                  78      0   100%
app\tools\__init__.py          3      0   100%
app\tools\base.py             16      0   100%
app\tools\observation.py     237    102    57%
app\tools\registry.py         30      1    97%
app\tools\side_effect.py      34      2    94%
app\verification.py           89      7    92%
app\worker.py                157     13    92%
----------------------------------------------
TOTAL                       1540    165    89%
137 passed, 1 skipped in 3.94s
```

- 收集用例 138 个：**137 passed，1 skipped**，失败 0。
- 总体行覆盖率 **89%**（1540 条语句，165 条未覆盖）。

## 用例分布表

| 测试文件 | 用例数 |
| --- | --- |
| tests/test_adversarial.py | 6 |
| tests/test_api.py | 12 |
| tests/test_architecture.py | 5 |
| tests/test_audit.py | 3 |
| tests/test_audit_replay.py | 5 |
| tests/test_boundary.py | 12 |
| tests/test_contract.py | 8 |
| tests/test_executor.py | 14 |
| tests/test_grounding.py | 8 |
| tests/test_health.py | 1 |
| tests/test_investigation.py | 5 |
| tests/test_llm_client.py | 6 |
| tests/test_models.py | 5 |
| tests/test_observation_tools.py | 14 |
| tests/test_policy_controller.py | 14 |
| tests/test_queue_worker.py | 13 |
| tests/test_verification.py | 7 |
| **合计** | **138** |

备注：`test_policy_controller.py` 由 1 个函数经 `@pytest.mark.parametrize` 展开为 14 个用例；`test_boundary.py`、`test_observation_tools.py` 中也含参数化用例，故用例数多于函数数。

## 跳过用例说明

1 个 skipped：`tests/test_boundary.py::test_reject_symlink_escape`，跳过原因（skip 条件原文）为 **`Windows 无符号链接测试环境`**（`@pytest.mark.skipif(os.name == "nt", reason="Windows 无符号链接测试环境")`）。该用例验证符号链接逃逸拒绝，需在 Linux 环境用 `os.symlink` 构造逃逸场景，Windows 开发机上按设计跳过；CI 或目标 Linux 环境会执行。

## 核心模块覆盖率 vs 85% 目标

| 核心模块 | 覆盖率 | 目标 | 结论 |
| --- | --- | --- | --- |
| app/policy（boundary.py 95% + controller.py 98% + rules.py 100% + \_\_init\_\_.py 100%，合计 90 条语句未覆盖 2 条） | **97.8%** | ≥85% | 达标 |
| app/executor.py | **93%** | ≥85% | 达标 |
| app/verification.py | **92%** | ≥85% | 达标 |
| app/audit.py | **100%** | ≥85% | 达标 |

四个核心模块全部达到 ≥85% 目标，**无核心模块需要标注待补强**。

非核心模块的诚实备注（后续可补强，不影响验收指标）：

- `app/tools/observation.py` 57%：Windows 分支（`run_powershell` 实调）与部分真实主机依赖路径未覆盖，现有用例集中在解析器与参数校验。
- `app/platform.py` 83%：超时与部分 Windows 子进程分支未覆盖。
- `app/db.py` 76%：`init_db` 与会话依赖的异常分支未覆盖。
